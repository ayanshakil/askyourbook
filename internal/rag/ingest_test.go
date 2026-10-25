package rag

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The C++ guide splits into 100 chunks over 40 pages.
const guideChunks, guidePages = 100, 40

func TestIngestBatchesWithBoundedConcurrency(t *testing.T) {
	s, emb, _, store := newTestService(Options{Workers: 3, BatchSize: 5})
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	emb.Delay = 20 * time.Millisecond // long enough for batches to overlap

	f, size := openPDF(t, "cpp_programming_guide.pdf")
	doc, err := s.Ingest(context.Background(), f, size, "guide.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Chunks != guideChunks {
		t.Fatalf("chunks = %d, want %d", doc.Chunks, guideChunks)
	}
	waitStatus(t, s, doc.ID, StatusReady)

	if got, want := emb.DocCalls(), guideChunks/5; got != want {
		t.Errorf("embedding calls = %d, want %d (one per batch of 5)", got, want)
	}
	// Exactly Workers: never more (the semaphore holds), and not fewer
	// (batches really do run concurrently).
	if got := emb.MaxInFlight(); got != 3 {
		t.Errorf("max concurrent embedding calls = %d, want 3", got)
	}
	pages := map[int]bool{}
	for _, p := range store.Points {
		if p.Payload["doc_id"] == doc.ID {
			pages[p.Payload["page"].(int)] = true
		}
	}
	if len(pages) != guidePages || !pages[1] || !pages[guidePages] {
		t.Errorf("stored chunks cover %d pages, want 1..%d", len(pages), guidePages)
	}
}

func TestIngestFirstErrorCancelsRemainingBatches(t *testing.T) {
	s, emb, _, store := newTestService(Options{Workers: 4, BatchSize: 5})
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	boom := errors.New("provider down")
	emb.Fail = func(call int) error {
		if call == 1 {
			return boom
		}
		return nil
	}
	emb.Delay = 10 * time.Second // other batches only finish if not cancelled

	f, size := openPDF(t, "cpp_programming_guide.pdf")
	start := time.Now()
	doc, err := s.Ingest(context.Background(), f, size, "guide.pdf")
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, doc.ID, StatusFailed)

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("failure took %v; in-flight batches weren't cancelled", elapsed)
	}
	// Only the batches already holding a semaphore slot ever started; the
	// other 16 saw the cancelled context and never called the embedder.
	if got := emb.DocCalls(); got > 4 {
		t.Errorf("embedding calls = %d, want at most 4 (the concurrency limit)", got)
	}
	if n, _ := store.Count(context.Background(), "", docFilter(doc.ID)); n != 0 {
		t.Errorf("%d chunks stored after failure", n)
	}
}

func TestEmbeddingLimitIsSharedAcrossDocuments(t *testing.T) {
	s, emb, _, _ := newTestService(Options{Workers: 2, BatchSize: 10, IngestJobs: 2, QueueSize: 2})
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	emb.Delay = 20 * time.Millisecond

	var ids []string
	for range 2 {
		f, size := openPDF(t, "cpp_programming_guide.pdf")
		doc, err := s.Ingest(context.Background(), f, size, "guide.pdf")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, doc.ID)
	}
	for _, id := range ids {
		waitStatus(t, s, id, StatusReady)
	}
	if got := emb.MaxInFlight(); got != 2 {
		t.Errorf("max concurrent embedding calls across two documents = %d, want 2", got)
	}
}
