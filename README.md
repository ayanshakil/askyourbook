# AskYourBook — chat with your PDF (Go + LangChainGo + Qdrant + OpenAI)

## How it works
**Ingest:** `POST /documents` → load PDF (one doc per page) → split into ~1000-char chunks
(150 overlap) → embed batches **concurrently** (bounded goroutine pool) → upsert into Qdrant
with payload `{doc_id, filename, page, text}`.

**Chat:** `POST /documents/{id}/chat` → embed the question with the **same** model →
cosine top-k search in Qdrant filtered by `doc_id` → stuff chunks into the prompt with
chat history → LLM answers with page citations.

## Run
```bash
cp .env.example .env                 # set GOOGLE_API_KEY (or LLM_PROVIDER=openai + OPENAI_API_KEY)
make up                              # Qdrant on :6333 (dashboard: /dashboard)
make run                             # API on :8080
```

| `LLM_PROVIDER` | Chat model | Embedding model | Dim | Qdrant collection |
|---|---|---|---|---|
| `gemini` (default) | gemini-2.5-flash | gemini-embedding-001 | 3072 | `askyourbook_gemini` |
| `openai` | gpt-4o-mini | text-embedding-3-small | 1536 | `askyourbook_openai` |

## Try it
```bash
curl -F "file=@testdata/sample.pdf" localhost:8080/documents
# -> {"document_id":"<ID>","chunks":212,...}

curl -X POST localhost:8080/documents/<ID>/chat \
  -H 'Content-Type: application/json' \
  -d '{"question":"What is chapter 3 about?"}'
# -> {"session_id":"<SID>","answer":"...","sources":[{"page":14,"score":0.82,...}]}

# follow-up in the same conversation
curl -X POST localhost:8080/documents/<ID>/chat \
  -H 'Content-Type: application/json' \
  -d '{"session_id":"<SID>","question":"Give me an example from it"}'
```

## Layout
```
cmd/server/main.go        wiring, config, graceful shutdown
internal/api/             REST handlers (net/http, Go 1.22 routing)
internal/rag/ingest.go    load → split → concurrent embed → upsert
internal/rag/chat.go      embed query → search → grounded LLM answer
internal/rag/service.go   service config + in-memory session store
internal/qdrant/          minimal Qdrant REST client
```

## Next steps (good interview talking points)
- **Query rewriting** for follow-ups ("what about it?") before retrieval
- **Streaming** answers via SSE
- **Async ingestion** for big PDFs: return 202 + job status endpoint
- Persist sessions in Redis; add `GET /documents`, `DELETE /documents/{id}`
- Retry with backoff on OpenAI 429s; unit tests with a fake embedder
