package rag

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/tmc/langchaingo/documentloaders"
	"github.com/tmc/langchaingo/schema"
	"github.com/tmc/langchaingo/textsplitter"

	"github.com/ayanshakil/askyourbook/internal/qdrant"
)

// Ingest loads a PDF, splits it into chunks, embeds the chunks concurrently,
// and upserts them into Qdrant. It returns the new document ID.
func (s *Service) Ingest(ctx context.Context, r io.ReaderAt, size int64, filename string) (string, int, error) {
	pages, err := documentloaders.NewPDF(r, size).Load(ctx) // one Document per page
	if err != nil {
		return "", 0, fmt.Errorf("load pdf: %w", err)
	}
	chunks, err := textsplitter.SplitDocuments(s.splitter, pages) // keeps page metadata
	if err != nil {
		return "", 0, fmt.Errorf("split: %w", err)
	}
	chunks = nonEmpty(chunks)
	if len(chunks) == 0 {
		return "", 0, errors.New("no extractable text (scanned PDF? needs OCR)")
	}

	docID := uuid.NewString()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		sem      = make(chan struct{}, s.workers) // bounds concurrency
		errOnce  sync.Once
		firstErr error
	)

	for start := 0; start < len(chunks); start += s.batchSize {
		batch := chunks[start:min(start+s.batchSize, len(chunks))]
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
	return s.qd.Upsert(ctx, s.collection, points)
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
