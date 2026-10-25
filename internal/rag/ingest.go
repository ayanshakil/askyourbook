package rag

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tmc/langchaingo/documentloaders"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/textsplitter"

	"github.com/ayanshakil/askyourbook/internal/qdrant"
)

// Ingest loads a PDF, splits it into chunks, embeds the chunks concurrently,
// and upserts them into Qdrant. It returns the new document ID.
func (s *Service) Ingest(ctx context.Context, r io.ReaderAt, size int64, filename string) (string, int, error) {
	start := time.Now()
	pages, err := documentloaders.NewPDF(r, size).Load(ctx) // one Document per page
	if err != nil {
		return "", 0, fmt.Errorf("%w: %v", ErrInvalidPDF, err)
	}
	chunks, err := textsplitter.SplitDocuments(s.splitter, pages) // keeps page metadata
	if err != nil {
		return "", 0, fmt.Errorf("split: %w", err)
	}
	chunks = nonEmpty(chunks)
	if len(chunks) == 0 {
		return "", 0, ErrNoText
	}

	docID := uuid.NewString()
	batches := (len(chunks) + s.batchSize - 1) / s.batchSize
	s.log.InfoContext(ctx, "ingest started", "doc_id", docID, "filename", filename,
		"pages", len(pages), "chunks", len(chunks), "batches", batches, "workers", s.workers)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, s.workers) // bounds concurrency
		errOnce  sync.Once
		firstErr error
	)

	for i := 0; i < len(chunks); i += s.batchSize {
		batch := chunks[i:min(i+s.batchSize, len(chunks))]
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			if err := s.embedAndStore(ctx, docID, filename, batch); err != nil {
				errOnce.Do(func() { firstErr = err; cancel() }) // fail fast
			}
		}()
	}
	wg.Wait()

	if firstErr != nil {
		return "", 0, firstErr
	}
	s.log.InfoContext(ctx, "ingest finished", "doc_id", docID, "chunks", len(chunks),
		"duration_ms", time.Since(start).Milliseconds())
	return docID, len(chunks), nil
}

func (s *Service) embedAndStore(ctx context.Context, docID, filename string, batch []schema.Document) error {
	texts := make([]string, len(batch))
	for i, d := range batch {
		texts[i] = d.PageContent
	}
	vecs, err := s.embedder.EmbedDocuments(ctx, texts)
	if err != nil {
		return fmt.Errorf("embed: %w", err)
	}
	if len(vecs) != len(batch) {
		return fmt.Errorf("embed: got %d vectors for %d chunks", len(vecs), len(batch))
	}

	points := make([]qdrant.Point, len(batch))
	for i, d := range batch {
		points[i] = qdrant.Point{
			ID:     uuid.NewString(),
			Vector: vecs[i],
			Payload: map[string]any{
				"doc_id":   docID,
				"filename": filename,
				"page":     d.Metadata["page"],
				"text":     d.PageContent,
			},
		}
	}
	if err := s.qd.Upsert(ctx, s.collection, points); err != nil {
		return fmt.Errorf("upsert: %w", err)
	}
	return nil
}

func nonEmpty(docs []schema.Document) []schema.Document {
	out := docs[:0]
	for _, d := range docs {
		if strings.TrimSpace(d.PageContent) != "" {
			out = append(out, d)
		}
	}
	return out
}
