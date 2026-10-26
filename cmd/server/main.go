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

	"github.com/redis/go-redis/v9"
	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/llms/googleai"
	"github.com/tmc/langchaingo/llms/openai"

	"github.com/ayanshakil/askyourbook/internal/api"
	"github.com/ayanshakil/askyourbook/internal/config"
	"github.com/ayanshakil/askyourbook/internal/logging"
	"github.com/ayanshakil/askyourbook/internal/qdrant"
	"github.com/ayanshakil/askyourbook/internal/rag"
	"github.com/ayanshakil/askyourbook/internal/retry"
	"github.com/ayanshakil/askyourbook/internal/session"
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
	policy := retry.Policy{MaxRetries: cfg.MaxRetries, BaseDelay: cfg.RetryBaseDelay, MaxDelay: cfg.RetryMaxDelay}

	qd := qdrant.New(cfg.QdrantURL)
	if err := qd.EnsureCollection(ctx, cfg.Collection, cfg.EmbeddingDim); err != nil {
		fatal(log, "qdrant", err)
	}

	sessions, closeSessions, err := newSessionStore(ctx, cfg)
	if err != nil {
		fatal(log, "sessions", err)
	}
	defer closeSessions()

	svc := rag.NewService(
		retry.NewChatModel(llm, policy, log),
		retry.NewEmbedder(embedder, policy, log),
		qd,
		rag.Options{
			Collection:   cfg.Collection,
			ChunkSize:    cfg.ChunkSize,
			ChunkOverlap: cfg.ChunkOverlap,
			Workers:      cfg.Workers,
			BatchSize:    cfg.BatchSize,
			IngestJobs:   cfg.IngestJobs,
			QueueSize:    cfg.IngestQueue,
			MaxChunks:    cfg.MaxChunks,
			Sessions:     sessions,
			Rewrite:      cfg.RewriteQueries,
			MinScore:     cfg.ScoreThreshold,
			Logger:       log,
		})
	handler := api.NewHandler(svc, api.Options{
		MaxUploadBytes: cfg.MaxUploadBytes,
		TopKDefault:    cfg.TopKDefault,
		TopKMax:        cfg.TopKMax,
		Logger:         log,
	})

	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: handler.Routes(),
		// Headers must arrive quickly; bodies get longer: an upload on a slow
		// link, or a chat whose LLM calls are retried, can take minutes.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute,
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}

	go func() {
		log.Info("AskYourBook listening", "addr", srv.Addr,
			"provider", cfg.Provider, "provider_chosen_by", cfg.ProviderSource,
			"chat_model", cfg.ChatModel, "embedding_model", cfg.EmbeddingModel, "collection", cfg.Collection,
			"session_store", cfg.SessionStore)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal(log, "http server", err)
		}
	}()

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	log.Info("shutting down")
	httpCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(httpCtx); err != nil {
		log.Error("http shutdown", "err", err)
	}
	// Let queued and running ingests finish; after the timeout they are
	// cancelled and their partial chunks removed.
	ingestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := svc.Shutdown(ingestCtx); err != nil {
		log.Error("ingest shutdown", "err", err)
	}
	log.Info("server stopped")
}

// newSessionStore returns the configured chat-history store and a func that
// releases it. A Redis store is pinged so a bad REDIS_URL fails at startup.
func newSessionStore(ctx context.Context, cfg config.Config) (rag.SessionStore, func(), error) {
	if cfg.SessionStore == "memory" {
		return session.NewMemory(cfg.HistoryLen, cfg.SessionTTL), func() {}, nil
	}
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, nil, fmt.Errorf("REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(opts)
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		rdb.Close()
		return nil, nil, fmt.Errorf("redis ping %s: %w", opts.Addr, err)
	}
	return session.NewRedis(rdb, cfg.HistoryLen, cfg.SessionTTL), func() { rdb.Close() }, nil
}

func fatal(log *slog.Logger, what string, err error) {
	log.Error(what, "err", err)
	os.Exit(1)
}

// newLLM returns the client for the configured provider.
func newLLM(ctx context.Context, cfg config.Config) (llmClient, error) {
	switch cfg.Provider {
	case "gemini":
		return googleai.New(ctx,
			googleai.WithAPIKey(string(cfg.APIKey)),
			googleai.WithDefaultModel(cfg.ChatModel),
			googleai.WithDefaultEmbeddingModel(cfg.EmbeddingModel),
		)
	case "openai":
		return openai.New(
			openai.WithToken(string(cfg.APIKey)),
			openai.WithModel(cfg.ChatModel),
			openai.WithEmbeddingModel(cfg.EmbeddingModel),
		)
	default:
		return nil, fmt.Errorf("unknown provider %q", cfg.Provider)
	}
}
