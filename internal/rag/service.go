package rag

import (
	"log/slog"
	"sync"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/textsplitter"
)

// Options are the Service tunables; see the config package for defaults.
type Options struct {
	Collection   string
	ChunkSize    int     // characters per chunk
	ChunkOverlap int     // characters shared by neighbouring chunks
	Workers      int     // max concurrent embedding goroutines
	BatchSize    int     // chunks per embedding request
	HistoryLen   int     // messages kept per chat session
	Rewrite      bool    // rewrite follow-up questions into standalone queries
	MinScore     float64 // drop hits below this cosine similarity; 0 disables
	Logger       *slog.Logger
}

type Service struct {
	llm        ChatModel
	embedder   Embedder
	qd         VectorStore
	collection string
	splitter   textsplitter.TextSplitter
	workers    int
	batchSize  int
	sessions   *SessionStore
	rewrite    bool
	minScore   float64
	log        *slog.Logger
}

func NewService(llm ChatModel, e Embedder, qd VectorStore, opts Options) *Service {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		llm:        llm,
		embedder:   e,
		qd:         qd,
		collection: opts.Collection,
		splitter: textsplitter.NewRecursiveCharacter(
			textsplitter.WithChunkSize(opts.ChunkSize),
			textsplitter.WithChunkOverlap(opts.ChunkOverlap),
		),
		workers:   opts.Workers,
		batchSize: opts.BatchSize,
		sessions:  NewSessionStore(opts.HistoryLen),
		rewrite:   opts.Rewrite,
		minScore:  opts.MinScore,
		log:       log,
	}
}

// SessionStore keeps per-session chat history in memory (swap for Redis later).
type SessionStore struct {
	mu      sync.Mutex
	maxMsgs int
	data    map[string][]llms.MessageContent
}

func NewSessionStore(maxMsgs int) *SessionStore {
	return &SessionStore{maxMsgs: maxMsgs, data: map[string][]llms.MessageContent{}}
}

func (s *SessionStore) Get(id string) []llms.MessageContent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]llms.MessageContent(nil), s.data[id]...)
}

func (s *SessionStore) Append(id string, msgs ...llms.MessageContent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := append(s.data[id], msgs...)
	if len(h) > s.maxMsgs {
		h = h[len(h)-s.maxMsgs:]
	}
	s.data[id] = h
}
