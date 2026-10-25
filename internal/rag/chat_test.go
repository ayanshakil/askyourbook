package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"
)

func ask(t *testing.T, s *Service, session, question string) Answer {
	t.Helper()
	ans, err := s.Chat(context.Background(), ChatRequest{DocID: "doc", SessionID: session, Question: question, TopK: 4})
	if err != nil {
		t.Fatalf("Chat(%q): %v", question, err)
	}
	return ans
}

func TestChatFirstQuestionIsNotRewritten(t *testing.T) {
	s, emb, chat, _ := newTestService(Options{Rewrite: true})
	ans := ask(t, s, "s1", "What happens to an unjoined std::thread?")

	if len(chat.calls) != 1 || isRewrite(chat.calls[0]) {
		t.Fatalf("want exactly one (answer) LLM call, got %d", len(chat.calls))
	}
	if emb.queries[0] != "What happens to an unjoined std::thread?" || ans.RewrittenQuery != "" {
		t.Fatalf("embedded %q, rewritten %q", emb.queries[0], ans.RewrittenQuery)
	}
	if len(ans.Sources) != 2 || ans.Sources[0].Page != 3 {
		t.Fatalf("sources = %+v", ans.Sources)
	}
}

func TestChatFollowUpIsRewritten(t *testing.T) {
	s, emb, chat, _ := newTestService(Options{Rewrite: true})
	ask(t, s, "s1", "What happens to an unjoined std::thread?")

	chat.rewriteReply = `"What to use instead of std::thread"`
	ans := ask(t, s, "s1", "What should I use instead?")

	if got := emb.queries[1]; got != "What to use instead of std::thread" {
		t.Errorf("embedded %q, want the rewritten query (quotes stripped)", got)
	}
	if ans.RewrittenQuery != "What to use instead of std::thread" {
		t.Errorf("RewrittenQuery = %q", ans.RewrittenQuery)
	}

	// The rewrite call sees the earlier turn; the answer call sees the
	// user's original wording, and so does the stored history.
	rewrite, answer := chat.calls[1], chat.calls[2]
	if !isRewrite(rewrite) || !strings.Contains(textOf(rewrite[1]), "unjoined std::thread") {
		t.Errorf("rewrite input = %q", textOf(rewrite[1]))
	}
	if last := textOf(answer[len(answer)-1]); !strings.Contains(last, "Question: What should I use instead?") {
		t.Errorf("answer prompt = %q", last)
	}
	hist := s.sessions.Get("doc:s1")
	if len(hist) != 4 || textOf(hist[2]) != "What should I use instead?" || hist[2].Role != llms.ChatMessageTypeHuman {
		t.Errorf("history = %v", hist)
	}
}

func TestChatRewriteFailureFallsBack(t *testing.T) {
	s, emb, chat, _ := newTestService(Options{Rewrite: true})
	ask(t, s, "s1", "first")
	chat.rewriteErr = errors.New("boom")
	ans := ask(t, s, "s1", "second")
	if emb.queries[1] != "second" || ans.RewrittenQuery != "" {
		t.Errorf("embedded %q, rewritten %q; want fallback to the question", emb.queries[1], ans.RewrittenQuery)
	}
}

func TestChatRewriteDisabled(t *testing.T) {
	s, emb, chat, _ := newTestService(Options{Rewrite: false})
	ask(t, s, "s1", "first")
	ask(t, s, "s1", "second")
	if len(chat.calls) != 2 || emb.queries[1] != "second" {
		t.Errorf("calls=%d embedded=%q; want no rewrite call", len(chat.calls), emb.queries[1])
	}
}

func TestChatUnknownDocument(t *testing.T) {
	s, _, chat, _ := newTestService(Options{})
	_, err := s.Chat(context.Background(), ChatRequest{DocID: "nope", SessionID: "s", Question: "q", TopK: 4})
	if !errors.Is(err, ErrDocumentNotFound) || len(chat.calls) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(chat.calls))
	}
}
