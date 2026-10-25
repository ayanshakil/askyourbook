package rag

import (
	"context"
	"fmt"
	"time"

	"github.com/ayanshakil/askyourbook/internal/qdrant"
)

// Document statuses.
const (
	StatusProcessing = "processing"
	StatusReady      = "ready"
	StatusFailed     = "failed"
)

// Document describes an uploaded PDF. While it is processing or after it
// failed, its status is held in memory; once ready it is derived from the
// chunks stored in Qdrant, so it survives server restarts.
type Document struct {
	ID        string    `json:"id"`
	Filename  string    `json:"filename"`
	Chunks    int       `json:"chunks"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`

	finishedAt time.Time // when a failed ingest stopped
}

// Document returns the document with the given ID, or ErrDocumentNotFound.
func (s *Service) Document(ctx context.Context, id string) (Document, error) {
	if d, ok := s.tracker.get(id); ok {
		return d, nil
	}
	filter := docFilter(id)
	recs, err := s.qd.Scroll(ctx, s.collection, filter, 1, "filename", "created_at")
	if err != nil {
		return Document{}, fmt.Errorf("look up document: %w", err)
	}
	if len(recs) == 0 {
		return Document{}, ErrDocumentNotFound
	}
	n, err := s.qd.Count(ctx, s.collection, filter)
	if err != nil {
		return Document{}, fmt.Errorf("count chunks: %w", err)
	}

	doc := Document{ID: id, Chunks: n, Status: StatusReady}
	doc.Filename, _ = recs[0].Payload["filename"].(string)
	if ts, ok := recs[0].Payload["created_at"].(string); ok {
		doc.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	}
	return doc, nil
}

// checkReady returns nil if the document can be chatted with,
// ErrDocumentNotReady if it is processing or failed, and
// ErrDocumentNotFound if it doesn't exist.
func (s *Service) checkReady(ctx context.Context, id string) error {
	if d, ok := s.tracker.get(id); ok {
		return fmt.Errorf("%w: status is %s", ErrDocumentNotReady, d.Status)
	}
	recs, err := s.qd.Scroll(ctx, s.collection, docFilter(id), 1)
	if err != nil {
		return fmt.Errorf("look up document: %w", err)
	}
	if len(recs) == 0 {
		return ErrDocumentNotFound
	}
	return nil
}

func docFilter(docID string) qdrant.Filter { return qdrant.Match("doc_id", docID) }
