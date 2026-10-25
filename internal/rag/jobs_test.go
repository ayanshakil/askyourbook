package rag

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func openSample(t *testing.T) (*os.File, int64) {
	t.Helper()
	f, err := os.Open("../../testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	st, _ := f.Stat()
	return f, st.Size()
}

// waitStatus polls until the document reaches want or the test times out.
func waitStatus(t *testing.T, s *Service, id, want string) Document {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		d, err := s.Document(context.Background(), id)
		if err == nil && d.Status == want {
			return d
		}
		if time.Now().After(deadline) {
			t.Fatalf("document %s: status %q (err %v), want %q", id, d.Status, err, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestIngestRunsInBackground(t *testing.T) {
	s, emb, _, store := newTestService(Options{})
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	emb.release = make(chan struct{})

	f, size := openSample(t)
	doc, err := s.Ingest(context.Background(), f, size, "sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != StatusProcessing || doc.Chunks != 6 {
		t.Fatalf("Ingest returned %+v", doc)
	}

	// While processing: GET reports it and chat is refused.
	if d, _ := s.Document(context.Background(), doc.ID); d.Status != StatusProcessing {
		t.Fatalf("status = %q, want processing", d.Status)
	}
	_, err = s.Chat(context.Background(), ChatRequest{DocID: doc.ID, SessionID: "s", Question: "q", TopK: 4})
	if !errors.Is(err, ErrDocumentNotReady) {
		t.Fatalf("chat while processing: err = %v", err)
	}

	close(emb.release)
	ready := waitStatus(t, s, doc.ID, StatusReady)
	if ready.Chunks != 6 || ready.Filename != "sample.pdf" {
		t.Fatalf("ready doc = %+v", ready)
	}
	if n, _ := store.Count(context.Background(), "", docFilter(doc.ID)); n != 6 {
		t.Fatalf("stored %d chunks, want 6", n)
	}
}

func TestIngestFailureIsRecordedAndCleanedUp(t *testing.T) {
	s, emb, _, store := newTestService(Options{BatchSize: 1, Workers: 1, ChunkSize: 1000, ChunkOverlap: 100})
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	emb.docErr = errors.New("provider down")

	f, size := openSample(t)
	doc, err := s.Ingest(context.Background(), f, size, "sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	failed := waitStatus(t, s, doc.ID, StatusFailed)
	if failed.Error == "" {
		t.Error("failed document has no error message")
	}
	if n, _ := store.Count(context.Background(), "", docFilter(doc.ID)); n != 0 {
		t.Errorf("%d chunks left behind after failure", n)
	}
	_, err = s.Chat(context.Background(), ChatRequest{DocID: doc.ID, SessionID: "s", Question: "q", TopK: 4})
	if !errors.Is(err, ErrDocumentNotReady) {
		t.Errorf("chat on failed doc: err = %v", err)
	}
}

func TestIngestQueueFull(t *testing.T) {
	s, emb, _, _ := newTestService(Options{IngestJobs: 1, QueueSize: 1})
	emb.release = make(chan struct{})
	t.Cleanup(func() { close(emb.release); s.Shutdown(context.Background()) })

	f, size := openSample(t)
	var errs []error
	for range 4 { // one running, one queued, the rest rejected
		_, err := s.Ingest(context.Background(), f, size, "sample.pdf")
		errs = append(errs, err)
		time.Sleep(20 * time.Millisecond) // let the worker pick up the first job
	}
	if errs[0] != nil || errs[1] != nil || !errors.Is(errs[2], ErrQueueFull) {
		t.Fatalf("errs = %v; want nil, nil, ErrQueueFull, ...", errs)
	}
}

func TestShutdownCancelsAfterTimeout(t *testing.T) {
	s, emb, _, store := newTestService(Options{})
	emb.release = make(chan struct{}) // never released: the ingest hangs

	f, size := openSample(t)
	doc, err := s.Ingest(context.Background(), f, size, "sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := s.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown = %v, want DeadlineExceeded", err)
	}
	if d, _ := s.Document(context.Background(), doc.ID); d.Status != StatusFailed {
		t.Errorf("status after forced shutdown = %q, want failed", d.Status)
	}
	if n, _ := store.Count(context.Background(), "", docFilter(doc.ID)); n != 0 {
		t.Errorf("%d chunks left behind", n)
	}
	if _, err := s.Ingest(context.Background(), f, size, "sample.pdf"); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Ingest after Shutdown: err = %v", err)
	}
}
