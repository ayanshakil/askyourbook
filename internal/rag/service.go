package rag

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/tmc/langchaingo/textsplitter"

	"github.com/ayanshakil/askyourbook/internal/session"
)

// defaultSessionTTL applies when Options.Sessions is nil.
const defaultSessionTTL = 24 * time.Hour

// Options are the Service tunables; see the config package for defaults.
type Options struct {
	Collection   string
	ChunkSize    int          // characters per chunk
	ChunkOverlap int          // characters shared by neighbouring chunks
	Workers      int          // max concurrent embedding calls, across all documents
	BatchSize    int          // chunks per embedding request
	IngestJobs   int          // documents indexed concurrently by background workers
	QueueSize    int          // documents that can wait for a worker
	HistoryLen   int          // messages kept per chat session (for the default store)
	Sessions     SessionStore // nil: in-memory store
	Rewrite      bool         // rewrite follow-up questions into standalone queries
	MinScore     float64      // drop hits below this cosine similarity; 0 disables
	Logger       *slog.Logger
}

type Service struct {
	llm        ChatModel
	embedder   Embedder
	qd         VectorStore
	collection string
	splitter   textsplitter.TextSplitter
	batchSize  int
	embedSem   chan struct{} // bounds concurrent embedding calls
	sessions   SessionStore
	rewrite    bool
	minScore   float64
	log        *slog.Logger

	// Background ingestion.
	tracker    *tracker
	jobs       chan job
	workersWG  sync.WaitGroup
	closeMu    sync.RWMutex
	closed     bool
	baseCtx    context.Context // cancelled to abort running ingests
	cancelBase context.CancelFunc
}

// NewService returns a Service and starts its background ingestion workers;
// call Shutdown to stop them.
func NewService(llm ChatModel, e Embedder, qd VectorStore, opts Options) *Service {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	sessions := opts.Sessions
	if sessions == nil {
		sessions = session.NewMemory(opts.HistoryLen, defaultSessionTTL)
	}
	baseCtx, cancel := context.WithCancel(context.Background())
	s := &Service{
		llm:        llm,
		embedder:   e,
		qd:         qd,
		collection: opts.Collection,
		splitter: textsplitter.NewRecursiveCharacter(
			textsplitter.WithChunkSize(opts.ChunkSize),
			textsplitter.WithChunkOverlap(opts.ChunkOverlap),
		),
		batchSize:  opts.BatchSize,
		embedSem:   make(chan struct{}, opts.Workers),
		sessions:   sessions,
		rewrite:    opts.Rewrite,
		minScore:   opts.MinScore,
		log:        log,
		tracker:    &tracker{docs: map[string]Document{}},
		jobs:       make(chan job, opts.QueueSize),
		baseCtx:    baseCtx,
		cancelBase: cancel,
	}
	s.startWorkers(opts.IngestJobs)
	return s
}
