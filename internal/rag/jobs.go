package rag

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/tmc/langchaingo/schema"
)

// failedTTL is how long a failed document's status stays queryable.
const failedTTL = time.Hour

type job struct {
	ctx    context.Context // carries request values (request ID), not cancellation
	doc    Document
	chunks []schema.Document
}

// tracker holds the status of documents that aren't ready: processing or
// failed. Ready documents live only in Qdrant, so they survive restarts.
type tracker struct {
	mu   sync.Mutex
	docs map[string]Document
}

func (t *tracker) get(id string) (Document, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.docs[id]
	return d, ok
}

func (t *tracker) set(d Document) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.docs[d.ID] = d
	t.pruneLocked()
}

func (t *tracker) remove(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.docs, id)
}

func (t *tracker) list() []Document {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked()
	out := make([]Document, 0, len(t.docs))
	for _, d := range t.docs {
		out = append(out, d)
	}
	return out
}

func (t *tracker) pruneLocked() {
	for id, d := range t.docs {
		if d.Status == StatusFailed && time.Since(d.finishedAt) > failedTTL {
			delete(t.docs, id)
		}
	}
}

// enqueue records the document as processing and hands it to the workers.
func (s *Service) enqueue(j job) error {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	if s.closed {
		return ErrShuttingDown
	}
	s.tracker.set(j.doc) // before queueing, so a fast worker can't finish first
	select {
	case s.jobs <- j:
		return nil
	default:
		s.tracker.remove(j.doc.ID)
		return ErrQueueFull
	}
}

func (s *Service) startWorkers(n int) {
	for range n {
		s.workersWG.Add(1)
		go func() {
			defer s.workersWG.Done()
			for j := range s.jobs {
				s.run(j)
			}
		}()
	}
}

func (s *Service) run(j job) {
	// Cancel the job if the service is shut down hard.
	ctx, cancel := context.WithCancel(j.ctx)
	defer cancel()
	stop := context.AfterFunc(s.baseCtx, cancel)
	defer stop()

	if err := s.index(ctx, j.doc, j.chunks); err != nil {
		s.log.ErrorContext(ctx, "ingest failed", "doc_id", j.doc.ID, "err", err)
		d := j.doc
		d.Status, d.Error, d.finishedAt = StatusFailed, "indexing failed: "+publicError(err), time.Now()
		s.tracker.set(d)
		return
	}
	s.tracker.remove(j.doc.ID) // now served from Qdrant as ready
}

// Shutdown stops accepting documents and waits for queued and running
// ingests to finish. If ctx expires first, running ingests are cancelled
// (their partial chunks are cleaned up) and Shutdown returns ctx.Err().
func (s *Service) Shutdown(ctx context.Context) error {
	s.closeMu.Lock()
	if !s.closed {
		s.closed = true
		close(s.jobs)
	}
	s.closeMu.Unlock()

	done := make(chan struct{})
	go func() { s.workersWG.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		s.cancelBase()
		<-done
		return ctx.Err()
	}
}

// publicError gives a short, client-safe reason for an ingest failure.
func publicError(err error) string {
	switch {
	case isContextErr(err):
		return "cancelled"
	default:
		return "the embedding provider or vector store returned an error"
	}
}

func isContextErr(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
