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

type ChatRequest struct {
	DocID     string
	SessionID string
	Question  string
	TopK      int
}

type Answer struct {
	Text    string
	Sources []Source
	// RewrittenQuery is the standalone query used for retrieval when a
	// follow-up question was rewritten; empty otherwise.
	RewrittenQuery string
}

// Chat retrieves the top-k chunks of the document most similar to the
// question and asks the LLM to answer grounded in them and prior turns.
func (s *Service) Chat(ctx context.Context, req ChatRequest) (Answer, error) {
	start := time.Now()
	if ok, err := s.exists(ctx, req.DocID); err != nil {
		return Answer{}, err
	} else if !ok {
		return Answer{}, ErrDocumentNotFound
	}

	key := req.DocID + ":" + req.SessionID
	history := s.sessions.Get(key)

	// Follow-ups like "what about the second one?" retrieve poorly on their
	// own, so they're rewritten into a standalone query before embedding.
	query := s.searchQuery(ctx, history, req.Question)
	qvec, err := s.embedder.EmbedQuery(ctx, query) // same model as ingestion
	if err != nil {
		return Answer{}, fmt.Errorf("embed query: %w", err)
	}
	hits, err := s.qd.Search(ctx, s.collection, qvec, req.TopK, docFilter(req.DocID))
	if err != nil {
		return Answer{}, fmt.Errorf("search: %w", err)
	}
	contextBlock, sources := buildContext(hits)

	msgs := []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt)}
	msgs = append(msgs, history...) // multi-turn memory
	msgs = append(msgs, llms.TextParts(llms.ChatMessageTypeHuman,
		fmt.Sprintf("Context:\n%s\nQuestion: %s", contextBlock, req.Question)))

	resp, err := s.llm.GenerateContent(ctx, msgs, llms.WithTemperature(0.2))
	if err != nil {
		return Answer{}, fmt.Errorf("llm: %w", err)
	}
	if len(resp.Choices) == 0 {
		return Answer{}, errors.New("llm returned no choices")
	}
	text := resp.Choices[0].Content
	s.logChat(ctx, req.DocID, hits, resp.Choices[0].GenerationInfo, start)

	// Store the user's original question (not the rewrite or the retrieved
	// context) to keep history small and faithful to the conversation.
	s.sessions.Append(key,
		llms.TextParts(llms.ChatMessageTypeHuman, req.Question),
		llms.TextParts(llms.ChatMessageTypeAI, text),
	)

	ans := Answer{Text: text, Sources: sources}
	if query != req.Question {
		ans.RewrittenQuery = query
	}
	return ans, nil
}

// buildContext formats the hits as page-labelled context for the prompt and
// as sources for the response.
func buildContext(hits []qdrant.SearchResult) (string, []Source) {
	var sb strings.Builder
	sources := make([]Source, 0, len(hits))
	for _, h := range hits {
		text, _ := h.Payload["text"].(string)
		page := pageOf(h.Payload)
		fmt.Fprintf(&sb, "[Page %d]\n%s\n\n", page, text)
		sources = append(sources, Source{Page: page, Score: h.Score, Snippet: truncate(text, 200)})
	}
	return sb.String(), sources
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
