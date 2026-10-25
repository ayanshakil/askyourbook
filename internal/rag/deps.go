package rag

import (
	"context"

	"github.com/tmc/langchaingo/llms"

	"github.com/ayanshakil/askyourbook/internal/qdrant"
)

// The Service depends only on these small interfaces so tests can use fakes
// and main can wrap the real clients (for example with retries).

// Embedder turns text into vectors. Documents and queries must be embedded
// by the same model, or their similarity scores are meaningless.
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// ChatModel generates a reply to a conversation.
type ChatModel interface {
	GenerateContent(ctx context.Context, msgs []llms.MessageContent, opts ...llms.CallOption) (*llms.ContentResponse, error)
}

// VectorStore stores chunk vectors and searches them. *qdrant.Client implements it.
type VectorStore interface {
	Upsert(ctx context.Context, collection string, points []qdrant.Point) error
	Search(ctx context.Context, collection string, vector []float32, k int, filter qdrant.Filter) ([]qdrant.SearchResult, error)
	Count(ctx context.Context, collection string, filter qdrant.Filter) (int, error)
	Scroll(ctx context.Context, collection string, filter qdrant.Filter, limit int, fields ...string) ([]qdrant.Record, error)
	Delete(ctx context.Context, collection string, filter qdrant.Filter) error
}

var _ VectorStore = (*qdrant.Client)(nil)
