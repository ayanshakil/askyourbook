package main

import (
	"context"
	"fmt"
	"log/slog"
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
	"github.com/ayanshakil/askyourbook/internal/config"
	"github.com/ayanshakil/askyourbook/internal/logging"
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

	cfg, err := config.Load()
	if err != nil {
		fatal(slog.Default(), "config", err)
	}
	log, err := logging.New(os.Stderr, cfg.LogFormat, cfg.LogLevel)
	if err != nil {
		fatal(slog.Default(), "logging", err)
	}
	slog.SetDefault(log)

	llm, err := newLLM(ctx, cfg)
	if err != nil {
		fatal(log, "llm", err)
	}
	embedder, err := embeddings.NewEmbedder(llm)
	if err != nil {
		fatal(log, "embedder", err)
	}

	qd := qdrant.New(cfg.QdrantURL)
	if err := qd.EnsureCollection(ctx, cfg.Collection, cfg.EmbeddingDim); err != nil {
		fatal(log, "qdrant", err)
	}

	svc := rag.NewService(llm, embedder, qd, rag.Options{
		Collection:   cfg.Collection,
		ChunkSize:    cfg.ChunkSize,
		ChunkOverlap: cfg.ChunkOverlap,
		Workers:      cfg.Workers,
		BatchSize:    cfg.BatchSize,
		HistoryLen:   cfg.HistoryLen,
		Logger:       log,
	})
	handler := api.NewHandler(svc, api.Options{
		MaxUploadBytes: cfg.MaxUploadBytes,
		TopKDefault:    cfg.TopKDefault,
		TopKMax:        cfg.TopKMax,
		Logger:         log,
	})

	srv := &http.Server{
		Addr:         ":" + cfg.Port,
		Handler:      handler.Routes(),
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	go func() {
		log.Info("AskYourBook listening", "addr", srv.Addr, "provider", cfg.Provider,
			"chat_model", cfg.ChatModel, "embedding_model", cfg.EmbeddingModel, "collection", cfg.Collection)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal(log, "http server", err)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
	log.Info("server stopped")
}

func fatal(log *slog.Logger, what string, err error) {
	log.Error(what, "err", err)
	os.Exit(1)
}

// newLLM returns the client for the configured provider. API keys are read
// from the environment (GOOGLE_API_KEY or OPENAI_API_KEY).
func newLLM(ctx context.Context, cfg config.Config) (llmClient, error) {
	switch cfg.Provider {
	case "gemini":
		return googleai.New(ctx,
			googleai.WithDefaultModel(cfg.ChatModel),
			googleai.WithDefaultEmbeddingModel(cfg.EmbeddingModel),
		)
	case "openai":
		return openai.New(
			openai.WithModel(cfg.ChatModel),
			openai.WithEmbeddingModel(cfg.EmbeddingModel),
		)
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
}
