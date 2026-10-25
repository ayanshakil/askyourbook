package retry

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/tmc/langchaingo/llms"
)

// flakyModel fails with failErr on its first call, optionally after
// streaming a chunk, and succeeds afterwards.
type flakyModel struct {
	calls          int
	streamThenFail bool
	failErr        error
}

func (m *flakyModel) GenerateContent(ctx context.Context, _ []llms.MessageContent, opts ...llms.CallOption) (*llms.ContentResponse, error) {
	m.calls++
	var o llms.CallOptions
	for _, opt := range opts {
		opt(&o)
	}
	if m.calls == 1 {
		if m.streamThenFail && o.StreamingFunc != nil {
			_ = o.StreamingFunc(ctx, []byte("partial "))
		}
		return nil, m.failErr
	}
	if o.StreamingFunc != nil {
		_ = o.StreamingFunc(ctx, []byte("full answer"))
	}
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: "full answer"}}}, nil
}

func newTestChatModel(m *flakyModel) *ChatModel {
	return NewChatModel(m, Policy{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
		slog.New(slog.DiscardHandler))
}

func TestStreamingRetriedBeforeFirstChunk(t *testing.T) {
	m := &flakyModel{failErr: httpErr{503}}
	var got string
	_, err := newTestChatModel(m).GenerateContent(context.Background(), nil,
		llms.WithStreamingFunc(func(_ context.Context, b []byte) error { got += string(b); return nil }))
	if err != nil || m.calls != 2 || got != "full answer" {
		t.Fatalf("err=%v calls=%d streamed=%q", err, m.calls, got)
	}
}

func TestStreamingNotRetriedAfterFirstChunk(t *testing.T) {
	m := &flakyModel{failErr: httpErr{503}, streamThenFail: true}
	var got string
	_, err := newTestChatModel(m).GenerateContent(context.Background(), nil,
		llms.WithStreamingFunc(func(_ context.Context, b []byte) error { got += string(b); return nil }))
	if err == nil || m.calls != 1 || got != "partial " {
		t.Fatalf("err=%v calls=%d streamed=%q; want one call and no replayed text", err, m.calls, got)
	}
	if code, _ := StatusCode(err); code != 503 {
		t.Errorf("returned error lost its cause: %v", err)
	}
}

func TestNonStreamingRetried(t *testing.T) {
	m := &flakyModel{failErr: httpErr{429}}
	resp, err := newTestChatModel(m).GenerateContent(context.Background(), nil)
	if err != nil || m.calls != 2 || resp.Choices[0].Content != "full answer" {
		t.Fatalf("err=%v calls=%d", err, m.calls)
	}
}
