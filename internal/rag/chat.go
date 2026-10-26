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

// notFoundAnswer is returned without calling the LLM when no chunk is similar enough.
const notFoundAnswer = "I couldn't find anything about that in the document."

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
	return s.chat(ctx, req, nil)
}

// ChatStream is like Chat, but passes each piece of the answer to onText as
// the LLM produces it. Errors returned before onText is first called happen
// before any output (e.g. ErrDocumentNotFound). If onText returns an error,
// generation stops and the turn is not stored.
func (s *Service) ChatStream(ctx context.Context, req ChatRequest, onText func(string) error) (Answer, error) {
	return s.chat(ctx, req, onText)
}

func (s *Service) chat(ctx context.Context, req ChatRequest, onText func(string) error) (Answer, error) {
	start := time.Now()
	if err := s.checkReady(ctx, req.DocID); err != nil {
		return Answer{}, err
	}

	key := req.DocID + ":" + req.SessionID
	history, err := s.sessions.Get(ctx, key)
	if err != nil {
		return Answer{}, fmt.Errorf("load history: %w", err)
	}

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
	hits = s.aboveThreshold(ctx, hits)

	ans := Answer{Sources: []Source{}}
	if query != req.Question {
		ans.RewrittenQuery = query
	}
	if len(hits) == 0 {
		// Nothing relevant: skip the LLM rather than let it answer from nothing.
		ans.Text = notFoundAnswer
		if onText != nil {
			if err := onText(ans.Text); err != nil {
				return Answer{}, err
			}
		}
		s.remember(ctx, key, req.Question, ans.Text)
		return ans, nil
	}
	contextBlock, sources := buildContext(hits)
	ans.Sources = sources

	msgs := []llms.MessageContent{llms.TextParts(llms.ChatMessageTypeSystem, systemPrompt)}
	msgs = append(msgs, history...) // multi-turn memory
	msgs = append(msgs, llms.TextParts(llms.ChatMessageTypeHuman,
		fmt.Sprintf("Context:\n%s\nQuestion: %s", contextBlock, req.Question)))

	opts := []llms.CallOption{llms.WithTemperature(0.2)}
	if onText != nil {
		opts = append(opts, llms.WithStreamingFunc(func(_ context.Context, chunk []byte) error {
			if len(chunk) == 0 {
				return nil // don't start a stream (and its 200) for nothing
			}
			return onText(string(chunk))
		}))
	}
	resp, err := s.llm.GenerateContent(ctx, msgs, opts...)
	if err != nil {
		return Answer{}, fmt.Errorf("llm: %w", err)
	}
	if len(resp.Choices) == 0 {
		return Answer{}, errors.New("llm returned no choices")
	}
	ans.Text = resp.Choices[0].Content
	if strings.TrimSpace(ans.Text) == "" {
		// e.g. a safety block, or Gemini spending its token budget on thinking.
		// Storing an empty turn could break later requests in the session.
		s.log.WarnContext(ctx, "empty answer", "doc_id", req.DocID, "stop_reason", resp.Choices[0].StopReason)
		return Answer{}, ErrEmptyAnswer
	}
	s.logChat(ctx, req.DocID, hits, resp.Choices[0].GenerationInfo, start)
	s.remember(ctx, key, req.Question, ans.Text)
	return ans, nil
}

// remember stores a turn. It keeps the user's original question (not the
// rewrite or the retrieved context) so history stays small and faithful.
// The answer has already been produced, so a storage error is only logged.
func (s *Service) remember(ctx context.Context, key, question, answer string) {
	err := s.sessions.Append(ctx, key,
		llms.TextParts(llms.ChatMessageTypeHuman, question),
		llms.TextParts(llms.ChatMessageTypeAI, answer),
	)
	if err != nil {
		s.log.ErrorContext(ctx, "save chat history", "session", key, "err", err)
	}
}

// aboveThreshold drops hits scoring below the configured minimum similarity.
// Hits arrive best first, so it keeps a prefix.
func (s *Service) aboveThreshold(ctx context.Context, hits []qdrant.SearchResult) []qdrant.SearchResult {
	if s.minScore <= 0 {
		return hits
	}
	n := 0
	for n < len(hits) && float64(hits[n].Score) >= s.minScore {
		n++
	}
	if n < len(hits) {
		attrs := []any{"kept", n, "dropped", len(hits) - n, "min_score", s.minScore}
		if n == 0 && len(hits) > 0 {
			attrs = append(attrs, "top_score", hits[0].Score)
		}
		s.log.InfoContext(ctx, "hits below score threshold", attrs...)
	}
	return hits[:n]
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
