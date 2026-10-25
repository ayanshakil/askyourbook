package rag

import (
	"context"
	"fmt"
	"strings"

	"github.com/tmc/langchaingo/llms"
)

const rewritePrompt = `You rewrite follow-up questions into standalone search queries for a document search engine.
Using the conversation, resolve references such as "it", "that", "instead" or "the second one" so the query
makes sense on its own. Keep the user's wording where possible and do not answer the question.
Reply with the query only: no quotes, labels or explanation. If the question is already standalone,
reply with it unchanged.`

const (
	maxRewriteTurnRunes = 500 // per message in the transcript sent for rewriting
	maxRewriteRunes     = 500 // longer outputs are treated as a failed rewrite
)

// searchQuery returns the query to embed for retrieval. Without history (or
// with rewriting disabled) that's the question itself; otherwise the LLM
// rewrites it into a standalone query. Any failure falls back to the question.
func (s *Service) searchQuery(ctx context.Context, history []llms.MessageContent, question string) string {
	if !s.rewrite || len(history) == 0 {
		return question
	}
	msgs := []llms.MessageContent{
		llms.TextParts(llms.ChatMessageTypeSystem, rewritePrompt),
		llms.TextParts(llms.ChatMessageTypeHuman, rewriteInput(history, question)),
	}
	resp, err := s.llm.GenerateContent(ctx, msgs, llms.WithTemperature(0))
	if err != nil {
		s.log.WarnContext(ctx, "query rewrite failed; using original question", "err", err)
		return question
	}
	if len(resp.Choices) == 0 {
		return question
	}
	query := strings.Trim(strings.TrimSpace(resp.Choices[0].Content), `"'`)
	if query == "" || len([]rune(query)) > maxRewriteRunes {
		s.log.WarnContext(ctx, "query rewrite unusable; using original question", "rewrite", truncate(query, 100))
		return question
	}
	s.log.InfoContext(ctx, "query rewritten", "question", question, "query", query)
	return query
}

// rewriteInput renders the conversation as a transcript. Passing it as one
// message (rather than as chat turns) keeps the model from answering it.
func rewriteInput(history []llms.MessageContent, question string) string {
	var sb strings.Builder
	sb.WriteString("Conversation:\n")
	for _, m := range history {
		role := "User"
		if m.Role == llms.ChatMessageTypeAI {
			role = "Assistant"
		}
		fmt.Fprintf(&sb, "%s: %s\n", role, truncate(textOf(m), maxRewriteTurnRunes))
	}
	fmt.Fprintf(&sb, "\nFollow-up question: %s\n\nStandalone search query:", question)
	return sb.String()
}

func textOf(m llms.MessageContent) string {
	var parts []string
	for _, p := range m.Parts {
		if t, ok := p.(llms.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, " ")
}
