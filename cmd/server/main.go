package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/llms/openai"

	"github.com/ayanshakil/askyourbook/internal/api"
	"github.com/ayanshakil/askyourbook/internal/qdrant"
	"github.com/ayanshakil/askyourbook/internal/rag"
)

const (
	collection   = "askyourbook"
	embeddingDim = 1536 // text-embedding-3-small
)

func main() {
	ctx := context.Background()

	// Reads OPENAI_API_KEY from the environment.
	llm, err := openai.New(
		openai.WithModel("gpt-4o-mini"),
		openai.WithEmbeddingModel("text-embedding-3-small"),
	)
	if err != nil {
		log.Fatalf("openai: %v", err)
	}
	embedder, err := embeddings.NewEmbedder(llm)
	if err != nil {
		log.Fatalf("embedder: %v", err)
	}

	qd := qdrant.New(getenv("QDRANT_URL", "http://localhost:6333"))
	if err := qd.EnsureCollection(ctx, collection, embeddingDim); err != nil {
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
		log.Printf("AskYourBook listening on %s", srv.Addr)
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

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
