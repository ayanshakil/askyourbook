// Package ragtest provides in-memory fakes of the rag service's
// dependencies (embedder, chat model, vector store) for tests. None of them
// touch the network.
package ragtest

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tmc/langchaingo/llms"

	"github.com/ayanshakil/askyourbook/internal/qdrant"
)

// Embedder returns a fixed vector for every text and records what it saw.
// Set its fields before use; they aren't safe to change concurrently.
type Embedder struct {
	// Each EmbedDocuments call runs these steps in order.
	//
	// Release, if non-nil, makes the call wait until it is closed (or the
	// context is cancelled). Fail, if set, is called with the call's 1-based
	// sequence number; a non-nil result is returned as the call's error.
	// Delay is how long the call then takes (cut short by cancellation).
	Release chan struct{}
	Fail    func(call int) error
	Delay   time.Duration

	mu          sync.Mutex
	queries     []string
	calls       atomic.Int32
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
}

func (e *Embedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	call := int(e.calls.Add(1))
	n := e.inFlight.Add(1)
	defer e.inFlight.Add(-1)
	for {
		m := e.maxInFlight.Load()
		if n <= m || e.maxInFlight.CompareAndSwap(m, n) {
			break
		}
	}

	if e.Release != nil {
		select {
		case <-e.Release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if e.Fail != nil {
		if err := e.Fail(call); err != nil {
			return nil, err
		}
	}
	if e.Delay > 0 {
		t := time.NewTimer(e.Delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{1, 0}
	}
	return out, nil
}

func (e *Embedder) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.queries = append(e.queries, text)
	return []float32{1, 0}, nil
}

// Queries returns the texts passed to EmbedQuery, in order.
func (e *Embedder) Queries() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.queries...)
}

// DocCalls is the number of EmbedDocuments calls so far.
func (e *Embedder) DocCalls() int { return int(e.calls.Load()) }

// MaxInFlight is the most EmbedDocuments calls that ran at the same time.
func (e *Embedder) MaxInFlight() int { return int(e.maxInFlight.Load()) }

// ChatModel answers Reply (default "the answer"), or RewriteReply /
// RewriteErr for query rewrite requests, streaming the reply in two pieces
// when asked to.
type ChatModel struct {
	Reply        *string
	RewriteReply string
	RewriteErr   error

	mu    sync.Mutex
	calls [][]llms.MessageContent
}

func (c *ChatModel) GenerateContent(ctx context.Context, msgs []llms.MessageContent, opts ...llms.CallOption) (*llms.ContentResponse, error) {
	c.mu.Lock()
	c.calls = append(c.calls, msgs)
	c.mu.Unlock()

	reply := "the answer"
	if c.Reply != nil {
		reply = *c.Reply
	}
	if IsRewrite(msgs) {
		if c.RewriteErr != nil {
			return nil, c.RewriteErr
		}
		reply = c.RewriteReply
	}
	var o llms.CallOptions
	for _, opt := range opts {
		opt(&o)
	}
	if o.StreamingFunc != nil {
		mid := len(reply) / 2
		for _, part := range []string{reply[:mid], reply[mid:]} {
			if err := o.StreamingFunc(ctx, []byte(part)); err != nil {
				return nil, err
			}
		}
	}
	return &llms.ContentResponse{Choices: []*llms.ContentChoice{{Content: reply}}}, nil
}

// Calls returns the messages of every GenerateContent call, in order.
func (c *ChatModel) Calls() [][]llms.MessageContent {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]llms.MessageContent(nil), c.calls...)
}

// IsRewrite reports whether msgs is a query-rewrite request (its system
// prompt starts with "You rewrite").
func IsRewrite(msgs []llms.MessageContent) bool {
	return len(msgs) > 0 && strings.HasPrefix(Text(msgs[0]), "You rewrite")
}

// Text returns the concatenated text parts of a message.
func Text(m llms.MessageContent) string {
	var parts []string
	for _, p := range m.Parts {
		if t, ok := p.(llms.TextContent); ok {
			parts = append(parts, t.Text)
		}
	}
	return strings.Join(parts, " ")
}

// Store is an in-memory vector store. Search ignores the vector and returns
// matching points in insertion order, scored from Scores (default 0.9).
// Filters must come from qdrant.Match("doc_id", ...).
type Store struct {
	Points []qdrant.Point
	Scores []float32

	mu    sync.Mutex
	lastK int
}

func (s *Store) Upsert(_ context.Context, _ string, pts []qdrant.Point) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Points = append(s.Points, pts...)
	return nil
}

func (s *Store) Search(_ context.Context, _ string, _ []float32, k int, filter qdrant.Filter) ([]qdrant.SearchResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastK = k
	var out []qdrant.SearchResult
	for i, p := range s.matching(filter) {
		if i == k {
			break
		}
		score := float32(0.9)
		if i < len(s.Scores) {
			score = s.Scores[i]
		}
		out = append(out, qdrant.SearchResult{ID: p.ID, Score: score, Payload: p.Payload})
	}
	return out, nil
}

func (s *Store) Count(_ context.Context, _ string, filter qdrant.Filter) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.matching(filter)), nil
}

func (s *Store) Scroll(_ context.Context, _ string, filter qdrant.Filter, limit int, _ ...string) ([]qdrant.Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []qdrant.Record
	for _, p := range s.matching(filter) {
		if len(out) == limit {
			break
		}
		out = append(out, qdrant.Record{ID: p.ID, Payload: p.Payload})
	}
	return out, nil
}

func (s *Store) Delete(_ context.Context, _ string, filter qdrant.Filter) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := filterValue(filter)
	keep := s.Points[:0]
	for _, p := range s.Points {
		if p.Payload["doc_id"] != want {
			keep = append(keep, p)
		}
	}
	s.Points = keep
	return nil
}

func (s *Store) Facet(_ context.Context, _, key string, limit int) ([]qdrant.FacetHit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	counts := map[string]int{}
	var order []string
	for _, p := range s.Points {
		v, _ := p.Payload[key].(string)
		if counts[v] == 0 {
			order = append(order, v)
		}
		counts[v]++
	}
	var out []qdrant.FacetHit
	for _, v := range order[:min(limit, len(order))] {
		out = append(out, qdrant.FacetHit{Value: v, Count: counts[v]})
	}
	return out, nil
}

// LastK is the k of the most recent Search.
func (s *Store) LastK() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastK
}

func (s *Store) matching(filter qdrant.Filter) []qdrant.Point {
	want := filterValue(filter)
	var out []qdrant.Point
	for _, p := range s.Points {
		if p.Payload["doc_id"] == want {
			out = append(out, p)
		}
	}
	return out
}

// filterValue extracts the matched value from a filter built by qdrant.Match.
func filterValue(filter qdrant.Filter) string {
	return filter["must"].([]any)[0].(map[string]any)["match"].(map[string]any)["value"].(string)
}

// DocPoints returns two stored chunks of a ready document with the given ID,
// on pages 3 and 4.
func DocPoints(docID string) []qdrant.Point {
	return []qdrant.Point{
		{ID: docID + "-1", Payload: map[string]any{"doc_id": docID, "filename": "book.pdf", "page": float64(3), "text": "std::jthread joins automatically."}},
		{ID: docID + "-2", Payload: map[string]any{"doc_id": docID, "filename": "book.pdf", "page": float64(4), "text": "Containers and iterators."}},
	}
}
