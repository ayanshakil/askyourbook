package rag

import (
	"log/slog"

	"github.com/ayanshakil/askyourbook/internal/ragtest"
)

// newTestService returns a Service over fakes with one stored document "doc".
func newTestService(opts Options) (*Service, *ragtest.Embedder, *ragtest.ChatModel, *ragtest.Store) {
	emb, chat, store := &ragtest.Embedder{}, &ragtest.ChatModel{}, &ragtest.Store{}
	store.Points = ragtest.DocPoints("doc")
	if opts.HistoryLen == 0 {
		opts.HistoryLen = 10
	}
	if opts.ChunkSize == 0 {
		opts.ChunkSize, opts.ChunkOverlap = 1000, 100
	}
	if opts.Workers == 0 {
		opts.Workers, opts.BatchSize = 2, 2
	}
	if opts.IngestJobs == 0 {
		opts.IngestJobs, opts.QueueSize = 1, 4
	}
	opts.Logger = slog.New(slog.DiscardHandler)
	return NewService(chat, emb, store, opts), emb, chat, store
}
