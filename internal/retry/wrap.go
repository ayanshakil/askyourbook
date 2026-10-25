package retry

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/tmc/langchaingo/llms"
)

type embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// Embedder wraps an embedder so both methods retry transient failures.
type Embedder struct {
	next   embedder
	policy Policy
	log    *slog.Logger
}

func NewEmbedder(next embedder, p Policy, log *slog.Logger) *Embedder {
	return &Embedder{next: next, policy: p, log: log}
}

func (e *Embedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	var out [][]float32
	err := Do(ctx, e.policy, logRetry(ctx, e.log, "embed documents"), func() (err error) {
		out, err = e.next.EmbedDocuments(ctx, texts)
		return err
	})
	return out, err
}

func (e *Embedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	var out []float32
	err := Do(ctx, e.policy, logRetry(ctx, e.log, "embed query"), func() (err error) {
		out, err = e.next.EmbedQuery(ctx, text)
		return err
	})
	return out, err
}

type chatModel interface {
	GenerateContent(ctx context.Context, msgs []llms.MessageContent, opts ...llms.CallOption) (*llms.ContentResponse, error)
}

// ChatModel wraps a chat model so GenerateContent retries transient failures.
// Streaming calls are retried only until the first chunk reaches the caller;
// after that a retry would replay text the caller has already received.
type ChatModel struct {
	next   chatModel
	policy Policy
	log    *slog.Logger
}

func NewChatModel(next chatModel, p Policy, log *slog.Logger) *ChatModel {
	return &ChatModel{next: next, policy: p, log: log}
}

func (m *ChatModel) GenerateContent(ctx context.Context, msgs []llms.MessageContent, opts ...llms.CallOption) (*llms.ContentResponse, error) {
	var o llms.CallOptions
	for _, opt := range opts {
		opt(&o)
	}
	var streamed atomic.Bool
	if next := o.StreamingFunc; next != nil {
		opts = append(slices.Clip(opts), llms.WithStreamingFunc(func(ctx context.Context, chunk []byte) error {
			streamed.Store(true)
			return next(ctx, chunk)
		}))
	}

	var out *llms.ContentResponse
	err := Do(ctx, m.policy, logRetry(ctx, m.log, "generate content"), func() (err error) {
		out, err = m.next.GenerateContent(ctx, msgs, opts...)
		if err != nil && streamed.Load() {
			return permanent{err}
		}
		return err
	})
	var p permanent
	if errors.As(err, &p) {
		err = p.error
	}
	return out, err
}

func logRetry(ctx context.Context, log *slog.Logger, op string) func(int, time.Duration, error) {
	return func(attempt int, wait time.Duration, err error) {
		code, _ := StatusCode(err)
		log.WarnContext(ctx, "retrying LLM call", "op", op, "attempt", attempt,
			"wait_ms", wait.Milliseconds(), "status", code, "err", err)
	}
}
