package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
	"github.com/tmc/langchaingo/llms/openai"

	"github.com/ayanshakil/askyourbook/internal/api"
	"github.com/ayanshakil/askyourbook/internal/qdrant"
	"github.com/ayanshakil/askyourbook/internal/rag"
)

// llmClient is a chat model that can also create embeddings. Ingestion and
// queries must be embedded by the same model, so one client provides both.
type llmClient interface {
	llms.Model
	embeddings.EmbedderClient
}

func main() {
	ctx := context.Background()

	provider := getenv("LLM_PROVIDER", "gemini")
	llm, dim, err := newLLM(ctx, provider)
	if err != nil {
		log.Fatalf("llm: %v", err)
	}
	embedder, err := embeddings.NewEmbedder(llm)
	if err != nil {
		log.Fatalf("embedder: %v", err)
	}

	// One collection per provider: their embedding spaces aren't comparable.
	collection := "askyourbook_" + provider
	qd := qdrant.New(getenv("QDRANT_URL", "http://localhost:6333"))
	if err := qd.EnsureCollection(ctx, collection, dim); err != nil {
		log.Fatalf("qdrant: %v", err)
	}

	svc := rag.NewService(llm, embedder, qd, collection)
	handler := api.NewHandler(svc)

	srv := &http.Server{
		Addr:         ":" + getenv("PORT", "8080"),
		Handler:      handler.Routes(),
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	go func() {
		log.Printf("AskYourBook listening on %s (provider %s, collection %s)", srv.Addr, provider, collection)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Println("server stopped")
}

// newLLM returns the client for the named provider and its embedding dimension.
func newLLM(ctx context.Context, provider string) (llmClient, int, error) {
	switch provider {
	case "gemini":
		// Reads GOOGLE_API_KEY from the environment.
		llm, err := googleai.New(ctx,
			googleai.WithDefaultModel("gemini-2.5-flash"),
			googleai.WithDefaultEmbeddingModel("gemini-embedding-001"),
		)
		return llm, 3072, err
	case "openai":
		// Reads OPENAI_API_KEY from the environment.
		llm, err := openai.New(
			openai.WithModel("gpt-4o-mini"),
			openai.WithEmbeddingModel("text-embedding-3-small"),
		)
		return llm, 1536, err
	default:
		return nil, 0, fmt.Errorf("unknown LLM_PROVIDER %q (want gemini or openai)", provider)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
