package rag

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
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

// MaxListedDocuments caps how many documents ListDocuments returns.
const MaxListedDocuments = 1000

// Document returns the document with the given ID, or ErrDocumentNotFound.
func (s *Service) Document(ctx context.Context, id string) (Document, error) {
	if d, ok := s.tracker.get(id); ok {
		return d, nil
	}
	n, err := s.qd.Count(ctx, s.collection, docFilter(id))
	if err != nil {
		return Document{}, fmt.Errorf("count chunks: %w", err)
	}
	if n == 0 {
		return Document{}, ErrDocumentNotFound
	}
	return s.readyDocument(ctx, id, n)
}

// readyDocument fills in an indexed document's details from one of its chunks.
func (s *Service) readyDocument(ctx context.Context, id string, chunks int) (Document, error) {
	recs, err := s.qd.Scroll(ctx, s.collection, docFilter(id), 1, "filename", "created_at")
	if err != nil {
		return Document{}, fmt.Errorf("look up document: %w", err)
	}
	if len(recs) == 0 {
		return Document{}, ErrDocumentNotFound // deleted meanwhile
	}
	doc := Document{ID: id, Chunks: chunks, Status: StatusReady}
	doc.Filename, _ = recs[0].Payload["filename"].(string)
	if ts, ok := recs[0].Payload["created_at"].(string); ok {
		doc.CreatedAt, _ = time.Parse(time.RFC3339, ts)
	}
	return doc, nil
}

// ListDocuments returns all documents, newest first: ready ones from Qdrant
// plus those still processing or recently failed.
func (s *Service) ListDocuments(ctx context.Context) ([]Document, error) {
	pending := s.tracker.list()
	docs := slices.Clone(pending)
	isPending := make(map[string]bool, len(pending))
	for _, d := range pending {
		isPending[d.ID] = true
	}

	hits, err := s.qd.Facet(ctx, s.collection, "doc_id", MaxListedDocuments)
	if err != nil {
		return nil, fmt.Errorf("list document ids: %w", err)
	}
	for _, h := range hits {
		if h.Count == 0 || isPending[h.Value] { // deleted, or partially indexed
			continue
		}
		doc, err := s.readyDocument(ctx, h.Value, h.Count)
		if errors.Is(err, ErrDocumentNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		docs = append(docs, doc)
	}
	slices.SortFunc(docs, func(a, b Document) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return docs, nil
}

// DeleteDocument removes a document and its chunks. A document that is
// still processing can't be deleted (ErrDocumentNotReady).
func (s *Service) DeleteDocument(ctx context.Context, id string) error {
	if d, ok := s.tracker.get(id); ok {
		if d.Status == StatusProcessing {
			return fmt.Errorf("%w: it is still processing", ErrDocumentNotReady)
		}
		s.tracker.remove(id) // failed: nothing is stored
		return nil
	}
	n, err := s.qd.Count(ctx, s.collection, docFilter(id))
	if err != nil {
		return fmt.Errorf("count chunks: %w", err)
	}
	if n == 0 {
		return ErrDocumentNotFound
	}
	if err := s.qd.Delete(ctx, s.collection, docFilter(id)); err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}
	s.log.InfoContext(ctx, "document deleted", "doc_id", id, "chunks", n)
	return nil
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
