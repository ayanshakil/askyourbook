package rag

import (
	"context"
	"log/slog"
	"strings"
	"sync"

	"github.com/tmc/langchaingo/llms"

	"github.com/ayanshakil/askyourbook/internal/qdrant"
)

// fakeEmbedder records the texts it embeds and returns a fixed vector.
// EmbedDocuments fails with docErr if set, and waits for release (if set)
// or cancellation first.
type fakeEmbedder struct {
	mu      sync.Mutex
	queries []string
	docErr  error
	release chan struct{}
}

func (f *fakeEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	if f.release != nil {
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.docErr != nil {
		return nil, f.docErr
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

func (f *fakeEmbedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queries = append(f.queries, text)
	return []float32{1, 0}, nil
}

// fakeChat answers rewrite requests with rewriteReply (or rewriteErr) and
// everything else with "answer N".
type fakeChat struct {
	mu           sync.Mutex
	calls        [][]llms.MessageContent
	rewriteReply string
	rewriteErr   error
}

func (f *fakeChat) GenerateContent(ctx context.Context, msgs []llms.MessageContent, opts ...llms.CallOption) (*llms.ContentResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, msgs)
	reply := "the answer"
	if isRewrite(msgs) {
		if f.rewriteErr != nil {
			return nil, f.rewriteErr
		}
		reply = f.rewriteReply
	}
	var o llms.CallOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.StreamingFunc != nil { // stream the reply in two pieces
		mid := len(reply) / 2
		for _, part := range []string{reply[:mid], reply[mid:]} {
			if err := o.StreamingFunc(ctx, []byte(part)); err != nil {
				return nil, err
			}
		}
	}
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: reply}}}, nil
}

func isRewrite(msgs []llms.MessageContent) bool {
	return len(msgs) > 0 && strings.HasPrefix(textOf(msgs[0]), "You rewrite")
}

// fakeStore holds points in memory; Search returns them in insertion order
// with the configured scores.
type fakeStore struct {
	mu     sync.Mutex
	points []qdrant.Point
	scores []float32
}

func (f *fakeStore) Upsert(_ context.Context, _ string, pts []qdrant.Point) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.points = append(f.points, pts...)
	return nil
}

// filterDocID extracts the doc_id from a filter built by qdrant.Match.
func filterDocID(filter qdrant.Filter) string {
	return filter["must"].([]any)[0].(map[string]any)["match"].(map[string]any)["value"].(string)
}

func (f *fakeStore) matching(filter qdrant.Filter) []qdrant.Point {
	want := filterDocID(filter)
	var out []qdrant.Point
	for _, p := range f.points {
		if p.Payload["doc_id"] == want {
			out = append(out, p)
		}
	}
	return out
}

func (f *fakeStore) Search(_ context.Context, _ string, _ []float32, k int, filter qdrant.Filter) ([]qdrant.SearchResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []qdrant.SearchResult
	for i, p := range f.matching(filter) {
		if i == k {
			break
		}
		score := float32(0.9)
		if i < len(f.scores) {
			score = f.scores[i]
		}
		out = append(out, qdrant.SearchResult{ID: p.ID, Score: score, Payload: p.Payload})
	}
	return out, nil
}

func (f *fakeStore) Count(_ context.Context, _ string, filter qdrant.Filter) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.matching(filter)), nil
}

func (f *fakeStore) Scroll(_ context.Context, _ string, filter qdrant.Filter, limit int, _ ...string) ([]qdrant.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []qdrant.Record
	for _, p := range f.matching(filter) {
		if len(out) == limit {
			break
		}
		out = append(out, qdrant.Record{ID: p.ID, Payload: p.Payload})
	}
	return out, nil
}

func (f *fakeStore) Delete(_ context.Context, _ string, filter qdrant.Filter) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	want := filterDocID(filter)
	keep := f.points[:0]
	for _, p := range f.points {
		if p.Payload["doc_id"] != want {
			keep = append(keep, p)
		}
	}
	f.points = keep
	return nil
}

// newTestService returns a Service over fakes with one stored document "doc".
func newTestService(opts Options) (*Service, *fakeEmbedder, *fakeChat, *fakeStore) {
	emb, chat, store := &fakeEmbedder{}, &fakeChat{}, &fakeStore{}
	store.points = []qdrant.Point{
		{ID: "1", Payload: map[string]any{"doc_id": "doc", "page": float64(3), "text": "std::jthread joins automatically."}},
		{ID: "2", Payload: map[string]any{"doc_id": "doc", "page": float64(4), "text": "Containers and iterators."}},
	}
	if opts.HistoryLen == 0 {
		opts.HistoryLen = 10
	}
	if opts.ChunkSize == 0 {
		opts.ChunkSize, opts.ChunkOverlap, opts.Workers, opts.BatchSize = 1000, 100, 2, 2
	}
	if opts.IngestJobs == 0 {
		opts.IngestJobs, opts.QueueSize = 1, 4
	}
	opts.Logger = slog.New(slog.DiscardHandler)
	return NewService(chat, emb, store, opts), emb, chat, store
}
