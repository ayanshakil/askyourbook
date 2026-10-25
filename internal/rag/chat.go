package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tmc/langchaingo/llms"

	"github.com/ayanshakil/askyourbook/internal/qdrant"
)

const systemPrompt = `You are AskYourBook, an assistant that answers questions about a PDF.
Answer ONLY from the provided context. Cite pages like (p. 12).
If the answer isn't in the context, say you couldn't find it in the document.`

type Source struct {
	Page    int     `json:"page"` // 1-based
	Score   float32 `json:"score"`
	Snippet string  `json:"snippet"`
}

// Chat embeds the question, retrieves the top-k chunks for this document,
// and asks the LLM to answer grounded in those chunks plus prior turns.
func (s *Service) Chat(ctx context.Context, docID, sessionID, question string, k int) (string, []Source, error) {
	start := time.Now()
	if ok, err := s.exists(ctx, docID); err != nil {
		return "", nil, err
	} else if !ok {
		return "", nil, ErrDocumentNotFound
	}
	qvec, err := s.embedder.EmbedQuery(ctx, question) // same model as ingestion
	if err != nil {
		return "", nil, fmt.Errorf("embed query: %w", err)
	}
	hits, err := s.qd.Search(ctx, s.collection, qvec, k, docFilter(docID))
	if err != nil {
		return "", nil, fmt.Errorf("search: %w", err)
	}

	var sb strings.Builder
	sources := make([]Source, 0, len(hits))
	for _, h := range hits {
		text, _ := h.Payload["text"].(string)
		page := pageOf(h.Payload)
		fmt.Fprintf(&sb, "[Page %d]\n%s\n\n", page, text)
		sources = append(sources, Source{Page: page, Score: h.Score, Snippet: truncate(text, 200)})
	}

	key := docID + ":" + sessionID
	msgs := []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt)}
	msgs = append(msgs, s.sessions.Get(key)...) // multi-turn memory
	msgs = append(msgs, llms.TextParts(llms.ChatMessageTypeHuman,
		fmt.Sprintf("Context:\n%s\nQuestion: %s", sb.String(), question)))

	resp, err := s.llm.GenerateContent(ctx, msgs, llms.WithTemperature(0.2))
	if err != nil {
		return "", nil, fmt.Errorf("llm: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", nil, errors.New("llm returned no choices")
	}
	answer := resp.Choices[0].Content
	s.logChat(ctx, docID, hits, resp.Choices[0].GenerationInfo, start)

	// Store the bare question (not the retrieved context) to keep history small.
	s.sessions.Append(key,
		llms.TextParts(llms.ChatMessageTypeHuman, question),
		llms.TextParts(llms.ChatMessageTypeAI, answer),
	)
	return answer, sources, nil
}

// pageOf reads the 1-based page number from a payload. JSON numbers decode
// as float64, so the int stored at ingest time comes back as a float.
func pageOf(payload map[string]any) int {
	switch p := payload["page"].(type) {
	case float64:
		return int(p)
	case int:
		return p
	}
	return 0
}

func (s *Service) logChat(ctx context.Context, docID string, hits []qdrant.SearchResult, info map[string]any, start time.Time) {
	attrs := []any{"doc_id", docID, "hits", len(hits), "duration_ms", time.Since(start).Milliseconds()}
	if len(hits) > 0 {
		attrs = append(attrs, "top_score", hits[0].Score)
	}
	// Both the googleai and openai clients report usage under these keys.
	// For Gemini 2.5, total also includes hidden "thinking" tokens.
	for _, k := range [][2]string{
		{"PromptTokens", "prompt_tokens"}, {"CompletionTokens", "completion_tokens"}, {"TotalTokens", "total_tokens"},
	} {
		if v, ok := info[k[0]]; ok {
			attrs = append(attrs, k[1], v)
		}
	}
	s.log.InfoContext(ctx, "chat answered", attrs...)
}

// truncate shortens s to at most n runes, never splitting a UTF-8 character.
func truncate(s string, n int) string {
	runes := 0
	for i := range s {
		if runes == n {
			return s[:i] + "…"
		}
		runes++
	}
	return s
}
