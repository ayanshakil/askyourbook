package rag

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

const systemPrompt = `You are AskYourBook, an assistant that answers questions about a PDF.
Answer ONLY from the provided context. Cite pages like (p. 12).
If the answer isn't in the context, say you couldn't find it in the document.`

type Source struct {
	Page    any     `json:"page"`
	Score   float32 `json:"score"`
	Snippet string  `json:"snippet"`
}

// Chat embeds the question, retrieves the top-k chunks for this document,
// and asks the LLM to answer grounded in those chunks plus prior turns.
func (s *Service) Chat(ctx context.Context, docID, sessionID, question string, k int) (string, []Source, error) {
	qvec, err := s.embedder.EmbedQuery(ctx, question) // same model as ingestion
	if err != nil {
		return "", nil, fmt.Errorf("embed query: %w", err)
	}
	hits, err := s.qd.Search(ctx, s.collection, qvec, k, docID)
	if err != nil {
		return "", nil, fmt.Errorf("search: %w", err)
	}

	var sb strings.Builder
	sources := make([]Source, 0, len(hits))
	for _, h := range hits {
		text, _ := h.Payload["text"].(string)
		fmt.Fprintf(&sb, "[Page %v]\n%s\n\n", h.Payload["page"], text)
		sources = append(sources, Source{Page: h.Payload["page"], Score: h.Score, Snippet: truncate(text, 200)})
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

	// Store the bare question (not the retrieved context) to keep history small.
	s.sessions.Append(key,
		llms.TextParts(llms.ChatMessageTypeHuman, question),
		llms.TextParts(llms.ChatMessageTypeAI, answer),
	)
	return answer, sources, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
