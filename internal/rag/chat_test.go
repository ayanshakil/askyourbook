package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tmc/langchaingo/llms"

	"github.com/ayanshakil/askyourbook/internal/ragtest"
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

	if len(chat.Calls()) != 1 || ragtest.IsRewrite(chat.Calls()[0]) {
		t.Fatalf("want exactly one (answer) LLM call, got %d", len(chat.Calls()))
	}
	if emb.Queries()[0] != "What happens to an unjoined std::thread?" || ans.RewrittenQuery != "" {
		t.Fatalf("embedded %q, rewritten %q", emb.Queries()[0], ans.RewrittenQuery)
	}
	if len(ans.Sources) != 2 || ans.Sources[0].Page != 3 {
		t.Fatalf("sources = %+v", ans.Sources)
	}
}

func TestChatFollowUpIsRewritten(t *testing.T) {
	s, emb, chat, _ := newTestService(Options{Rewrite: true})
	ask(t, s, "s1", "What happens to an unjoined std::thread?")

	chat.RewriteReply = `"What to use instead of std::thread"`
	ans := ask(t, s, "s1", "What should I use instead?")

	if got := emb.Queries()[1]; got != "What to use instead of std::thread" {
		t.Errorf("embedded %q, want the rewritten query (quotes stripped)", got)
	}
	if ans.RewrittenQuery != "What to use instead of std::thread" {
		t.Errorf("RewrittenQuery = %q", ans.RewrittenQuery)
	}

	// The rewrite call sees the earlier turn; the answer call sees the
	// user's original wording, and so does the stored history.
	rewrite, answer := chat.Calls()[1], chat.Calls()[2]
	if !ragtest.IsRewrite(rewrite) || !strings.Contains(textOf(rewrite[1]), "unjoined std::thread") {
		t.Errorf("rewrite input = %q", textOf(rewrite[1]))
	}
	if last := textOf(answer[len(answer)-1]); !strings.Contains(last, "Question: What should I use instead?") {
		t.Errorf("answer prompt = %q", last)
	}
	hist := history(t, s, "doc:s1")
	if len(hist) != 4 || textOf(hist[2]) != "What should I use instead?" || hist[2].Role != llms.ChatMessageTypeHuman {
		t.Errorf("history = %v", hist)
	}
}

func TestChatRewriteFailureFallsBack(t *testing.T) {
	s, emb, chat, _ := newTestService(Options{Rewrite: true})
	ask(t, s, "s1", "first")
	chat.RewriteErr = errors.New("boom")
	ans := ask(t, s, "s1", "second")
	if emb.Queries()[1] != "second" || ans.RewrittenQuery != "" {
		t.Errorf("embedded %q, rewritten %q; want fallback to the question", emb.Queries()[1], ans.RewrittenQuery)
	}
}

func TestChatRewriteDisabled(t *testing.T) {
	s, emb, chat, _ := newTestService(Options{Rewrite: false})
	ask(t, s, "s1", "first")
	ask(t, s, "s1", "second")
	if len(chat.Calls()) != 2 || emb.Queries()[1] != "second" {
		t.Errorf("calls=%d embedded=%q; want no rewrite call", len(chat.Calls()), emb.Queries()[1])
	}
}

func TestChatUnknownDocument(t *testing.T) {
	s, _, chat, _ := newTestService(Options{})
	_, err := s.Chat(context.Background(), ChatRequest{DocID: "nope", SessionID: "s", Question: "q", TopK: 4})
	if !errors.Is(err, ErrDocumentNotFound) || len(chat.Calls()) != 0 {
		t.Fatalf("err=%v calls=%d", err, len(chat.Calls()))
	}
}

func TestChatScoreThresholdDropsWeakHits(t *testing.T) {
	s, _, chat, store := newTestService(Options{MinScore: 0.6})
	store.Scores = []float32{0.8, 0.5}
	ans := ask(t, s, "s1", "q")
	if len(ans.Sources) != 1 || ans.Sources[0].Score != 0.8 || len(chat.Calls()) != 1 {
		t.Fatalf("sources=%+v calls=%d", ans.Sources, len(chat.Calls()))
	}
}

func TestChatNothingAboveThresholdSkipsLLM(t *testing.T) {
	s, _, chat, store := newTestService(Options{MinScore: 0.6})
	store.Scores = []float32{0.55, 0.4}
	ans := ask(t, s, "s1", "q")
	if ans.Text != notFoundAnswer || len(ans.Sources) != 0 || ans.Sources == nil {
		t.Fatalf("answer=%q sources=%#v", ans.Text, ans.Sources)
	}
	if len(chat.Calls()) != 0 {
		t.Fatalf("LLM called %d times; want 0", len(chat.Calls()))
	}
	if h := history(t, s, "doc:s1"); len(h) != 2 || textOf(h[1]) != notFoundAnswer {
		t.Fatalf("history = %v; want the turn recorded", h)
	}
}

func TestChatStreamDeliversChunksAndStoresTurn(t *testing.T) {
	s, _, _, _ := newTestService(Options{})
	var chunks []string
	ans, err := s.ChatStream(context.Background(), ChatRequest{DocID: "doc", SessionID: "s1", Question: "q", TopK: 4},
		func(text string) error { chunks = append(chunks, text); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 2 || strings.Join(chunks, "") != ans.Text || ans.Text != "the answer" {
		t.Fatalf("chunks=%q answer=%q", chunks, ans.Text)
	}
	if len(ans.Sources) != 2 {
		t.Fatalf("sources = %+v", ans.Sources)
	}
	if h := history(t, s, "doc:s1"); len(h) != 2 || textOf(h[1]) != "the answer" {
		t.Fatalf("history = %v", h)
	}
}

func TestChatStreamAbortedByCallerIsNotStored(t *testing.T) {
	s, _, _, _ := newTestService(Options{})
	gone := errors.New("client went away")
	_, err := s.ChatStream(context.Background(), ChatRequest{DocID: "doc", SessionID: "s1", Question: "q", TopK: 4},
		func(string) error { return gone })
	if !errors.Is(err, gone) {
		t.Fatalf("err = %v", err)
	}
	if h := history(t, s, "doc:s1"); len(h) != 0 {
		t.Fatalf("history = %v; an aborted turn must not be stored", h)
	}
}

func TestChatStreamNotFoundIsStreamed(t *testing.T) {
	s, _, chat, store := newTestService(Options{MinScore: 0.6})
	store.Scores = []float32{0.1, 0.1}
	var got string
	_, err := s.ChatStream(context.Background(), ChatRequest{DocID: "doc", SessionID: "s1", Question: "q", TopK: 4},
		func(text string) error { got += text; return nil })
	if err != nil || got != notFoundAnswer || len(chat.Calls()) != 0 {
		t.Fatalf("err=%v streamed=%q calls=%d", err, got, len(chat.Calls()))
	}
}

func history(t *testing.T, s *Service, key string) []llms.MessageContent {
	t.Helper()
	h, err := s.sessions.Get(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestChatEmptyAnswerIsAnErrorAndNotStored(t *testing.T) {
	s, _, chat, _ := newTestService(Options{})
	blank := "  \n"
	chat.Reply = &blank
	for _, stream := range []bool{false, true} {
		req := ChatRequest{DocID: "doc", SessionID: "s1", Question: "q", TopK: 4}
		var err error
		if stream {
			_, err = s.ChatStream(context.Background(), req, func(string) error { return nil })
		} else {
			_, err = s.Chat(context.Background(), req)
		}
		if !errors.Is(err, ErrEmptyAnswer) {
			t.Fatalf("stream=%v: err = %v, want ErrEmptyAnswer", stream, err)
		}
	}
	if h := history(t, s, "doc:s1"); len(h) != 0 {
		t.Fatalf("history = %v; empty turns must not be stored", h)
	}
}
