package rag

import (
	"sync"

	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/textsplitter"

	"github.com/yourname/askyourbook/internal/qdrant"
)

type Service struct {
	llm        llms.Model
	embedder   embeddings.Embedder
	qd         *qdrant.Client
	collection string
	splitter   textsplitter.TextSplitter
	workers    int // max concurrent embedding goroutines
	batchSize  int // chunks per embedding request
	sessions   *SessionStore
}

func NewService(llm llms.Model, e embeddings.Embedder, qd *qdrant.Client, collection string) *Service {
	return &Service{
		llm:        llm,
		embedder:   e,
		qd:         qd,
		collection: collection,
		splitter: textsplitter.NewRecursiveCharacter(
			textsplitter.WithChunkSize(1000),
			textsplitter.WithChunkOverlap(150),
		),
		workers:   4,
		batchSize: 32,
		sessions:  NewSessionStore(10),
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
