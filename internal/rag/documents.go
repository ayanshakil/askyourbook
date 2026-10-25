package rag

import (
	"context"
	"fmt"
	"time"

	"github.com/ayanshakil/askyourbook/internal/qdrant"
)

// Document describes an ingested PDF. It is derived from the chunks stored in
// Qdrant, so it survives server restarts without a separate database.
type Document struct {
	ID        string    `json:"id"`
	Filename  string    `json:"filename"`
	Chunks    int       `json:"chunks"`
	CreatedAt time.Time `json:"created_at"`
}

// Document returns the document with the given ID, or ErrDocumentNotFound.
func (s *Service) Document(ctx context.Context, id string) (Document, error) {
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

	doc := Document{ID: id, Chunks: n}
	doc.Filename, _ = recs[0].Payload["filename"].(string)
	if ts, ok := recs[0].Payload["created_at"].(string); ok {
		doc.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	}
	return doc, nil
}

// exists reports whether any chunks are stored for the document.
func (s *Service) exists(ctx context.Context, id string) (bool, error) {
	recs, err := s.qd.Scroll(ctx, s.collection, docFilter(id), 1)
	if err != nil {
		return false, fmt.Errorf("look up document: %w", err)
	}
	return len(recs) > 0, nil
}

func docFilter(docID string) qdrant.Filter { return qdrant.Match("doc_id", docID) }
