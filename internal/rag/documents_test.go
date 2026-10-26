package rag

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ayanshakil/askyourbook/internal/qdrant"
	"github.com/ayanshakil/askyourbook/internal/ragtest"
)

func TestListDocumentsMergesPendingAndReady(t *testing.T) {
	s, emb, _, store := newTestService(Options{})
	emb.Release = make(chan struct{})
	t.Cleanup(func() { close(emb.Release); s.Shutdown(context.Background()) })

	store.Points = append(store.Points,
		qdrant.Point{ID: "3", Payload: map[string]any{"doc_id": "older", "filename": "a.pdf", "created_at": "2026-01-01T00:00:00Z"}},
		qdrant.Point{ID: "4", Payload: map[string]any{"doc_id": "newer", "filename": "b.pdf", "created_at": "2026-06-01T00:00:00Z"}},
		qdrant.Point{ID: "5", Payload: map[string]any{"doc_id": "newer", "filename": "b.pdf", "created_at": "2026-06-01T00:00:00Z"}},
	)
	f, size := openSample(t)
	pending, err := s.Ingest(context.Background(), f, size, "sample.pdf")
	if err != nil {
		t.Fatal(err)
	}

	docs, err := s.ListDocuments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// Newest first: the processing upload (created now), then newer, older,
	// then "doc" from the fixture, which has no created_at.
	var got []string
	for _, d := range docs {
		got = append(got, d.ID+":"+d.Status)
	}
	want := []string{pending.ID + ":processing", "newer:ready", "older:ready", "doc:ready"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if docs[1].Chunks != 2 || docs[1].Filename != "b.pdf" {
		t.Errorf("newer = %+v", docs[1])
	}
}

func TestDeleteDocument(t *testing.T) {
	s, emb, _, store := newTestService(Options{})
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	ctx := context.Background()

	if err := s.DeleteDocument(ctx, "doc"); err != nil {
		t.Fatalf("delete ready doc: %v", err)
	}
	if n, _ := store.Count(ctx, "", docFilter("doc")); n != 0 {
		t.Fatalf("%d chunks left", n)
	}
	if _, err := s.Document(ctx, "doc"); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("after delete: err = %v", err)
	}
	if err := s.DeleteDocument(ctx, "doc"); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("second delete: err = %v", err)
	}

	// A processing document can't be deleted; a failed one can.
	emb.Release = make(chan struct{})
	f, size := openSample(t)
	doc, _ := s.Ingest(ctx, f, size, "sample.pdf")
	if err := s.DeleteDocument(ctx, doc.ID); !errors.Is(err, ErrDocumentNotReady) {
		t.Fatalf("delete while processing: err = %v", err)
	}
	emb.Fail = func(int) error { return errors.New("boom") }
	close(emb.Release)
	waitStatus(t, s, doc.ID, StatusFailed)
	if err := s.DeleteDocument(ctx, doc.ID); err != nil {
		t.Fatalf("delete failed doc: %v", err)
	}
	if _, err := s.Document(ctx, doc.ID); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("failed doc still listed: %v", err)
	}
}

func TestIncompleteDocumentIsNotReady(t *testing.T) {
	s, _, chat, store := newTestService(Options{})
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	ctx := context.Background()
	// Two of six chunks survived, as after a crash mid-ingest.
	for _, p := range ragtest.DocPoints("partial") {
		p.Payload["total_chunks"] = 6
		store.Points = append(store.Points, p)
	}

	doc, err := s.Document(ctx, "partial")
	if err != nil {
		t.Fatal(err)
	}
	if doc.Status != StatusFailed || !strings.Contains(doc.Error, "2 of 6") {
		t.Fatalf("doc = %+v, want failed and incomplete", doc)
	}
	_, err = s.Chat(ctx, ChatRequest{DocID: "partial", SessionID: "s", Question: "q", TopK: 4})
	if !errors.Is(err, ErrDocumentNotReady) || len(chat.Calls()) != 0 {
		t.Fatalf("chat on incomplete doc: err=%v calls=%d", err, len(chat.Calls()))
	}
	if err := s.DeleteDocument(ctx, "partial"); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.Count(ctx, "", docFilter("partial")); n != 0 {
		t.Fatalf("%d chunks left", n)
	}

	// A complete document (all chunks present) is ready.
	for _, p := range ragtest.DocPoints("whole") {
		p.Payload["total_chunks"] = 2
		store.Points = append(store.Points, p)
	}
	if doc, _ := s.Document(ctx, "whole"); doc.Status != StatusReady {
		t.Fatalf("complete doc status = %q", doc.Status)
	}
}

func TestDeleteFailedDocumentRemovesLeftoverChunks(t *testing.T) {
	s, emb, _, store := newTestService(Options{})
	t.Cleanup(func() { s.Shutdown(context.Background()) })
	ctx := context.Background()
	emb.Fail = func(int) error { return errors.New("boom") }

	f, size := openSample(t)
	doc, err := s.Ingest(ctx, f, size, "sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, s, doc.ID, StatusFailed)
	// Simulate a cleanup that failed and left chunks behind.
	store.Points = append(store.Points, ragtest.DocPoints(doc.ID)...)

	if err := s.DeleteDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.Count(ctx, "", docFilter(doc.ID)); n != 0 {
		t.Fatalf("%d leftover chunks survived the delete", n)
	}
	if _, err := s.Document(ctx, doc.ID); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("after delete: %v", err)
	}
}
