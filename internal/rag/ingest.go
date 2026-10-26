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

// Ingest parses and chunks a PDF, then queues the chunks to be embedded and
// indexed by a background worker. Parsing happens here, in the caller's
// request, so unreadable or text-less PDFs are rejected immediately; the
// slow, network-bound part runs asynchronously. The returned document has
// status "processing"; Document reports when it is ready or failed.
func (s *Service) Ingest(ctx context.Context, r io.ReaderAt, size int64, filename string) (Document, error) {
	// Parsing holds the whole text in memory, so only a few run at once.
	select {
	case s.parseSem <- struct{}{}:
	case <-ctx.Done():
		return Document{}, ctx.Err()
	}
	pages, err := documentloaders.NewPDF(r, size).Load(ctx) // one Document per page
	<-s.parseSem
	if err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrInvalidPDF, err)
	}
	chunks, err := textsplitter.SplitDocuments(s.splitter, pages) // keeps page metadata
	if err != nil {
		return Document{}, fmt.Errorf("split: %w", err)
	}
	chunks = nonEmpty(chunks)
	if len(chunks) == 0 {
		return Document{}, ErrNoText
	}
	if len(chunks) > s.maxChunks {
		return Document{}, fmt.Errorf("%w: %d chunks (about %d pages); the limit is %d chunks",
			ErrTooManyChunks, len(chunks), len(pages), s.maxChunks)
	}

	doc := Document{
		ID:        uuid.NewString(),
		Filename:  filename,
		Chunks:    len(chunks),
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Status:    StatusProcessing,
	}
	// The job outlives the request, so it keeps the request's values (for
	// the request ID in logs) but not its cancellation.
	if err := s.enqueue(job{ctx: context.WithoutCancel(ctx), doc: doc, chunks: chunks}); err != nil {
		return Document{}, err
	}
	s.log.InfoContext(ctx, "ingest queued", "doc_id", doc.ID, "filename", filename,
		"pages", len(pages), "chunks", len(chunks))
	return doc, nil
}

// index embeds the chunks concurrently and upserts them into Qdrant. Each
// batch runs in its own goroutine; the service-wide semaphore caps how many
// embedding calls are in flight across all documents, and the first error
// cancels the remaining batches. On failure, stored chunks are deleted.
func (s *Service) index(ctx context.Context, doc Document, chunks []schema.Document) error {
	start := time.Now()
	batches := (len(chunks) + s.batchSize - 1) / s.batchSize
	s.log.InfoContext(ctx, "ingest started", "doc_id", doc.ID, "chunks", len(chunks),
		"batches", batches, "max_concurrent", cap(s.embedSem))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		errOnce  sync.Once
		firstErr error
	)
	createdAt := doc.CreatedAt.Format(time.RFC3339)
	for i := 0; i < len(chunks); i += s.batchSize {
		batch := chunks[i:min(i+s.batchSize, len(chunks))]
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case s.embedSem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-s.embedSem }()
			// select picks randomly when a slot frees up just as another
			// batch fails, so re-check before starting the call.
			if ctx.Err() != nil {
				return
			}

			if err := s.embedAndStore(ctx, doc, createdAt, batch); err != nil {
				errOnce.Do(func() { firstErr = err; cancel() }) // fail fast
			}
		}()
	}
	wg.Wait()

	if firstErr == nil && ctx.Err() != nil {
		firstErr = ctx.Err() // cancelled before any batch failed (e.g. shutdown)
	}
	if firstErr != nil {
		s.cleanup(ctx, doc.ID)
		return firstErr
	}
	s.log.InfoContext(ctx, "ingest finished", "doc_id", doc.ID, "chunks", len(chunks),
		"duration_ms", time.Since(start).Milliseconds())
	return nil
}

// cleanup deletes the chunks of a failed ingest so the document never
// appears half-indexed. It runs even if ctx was cancelled.
func (s *Service) cleanup(ctx context.Context, docID string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	if err := s.qd.Delete(ctx, s.collection, docFilter(docID)); err != nil {
		s.log.ErrorContext(ctx, "cleanup after failed ingest", "doc_id", docID, "err", err)
	}
}

func (s *Service) embedAndStore(ctx context.Context, doc Document, createdAt string, batch []schema.Document) error {
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
				"doc_id":       doc.ID,
				"filename":     doc.Filename,
				"created_at":   createdAt,
				"total_chunks": doc.Chunks, // lets lookups detect a partial ingest
				"page":         d.Metadata["page"],
				"text":         d.PageContent,
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
