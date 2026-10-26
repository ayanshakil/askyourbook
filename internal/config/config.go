// Package config loads AskYourBook's settings from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config holds every tunable. Zero values are never used: Load fills in defaults.
type Config struct {
	Port      string // PORT
	QdrantURL string // QDRANT_URL
	LogFormat string // LOG_FORMAT: text or json
	LogLevel  string // LOG_LEVEL: debug, info, warn or error

	Provider       string // LLM_PROVIDER: gemini or openai
	ChatModel      string // CHAT_MODEL
	EmbeddingModel string // EMBEDDING_MODEL
	EmbeddingDim   int    // EMBEDDING_DIM; must match EMBEDDING_MODEL
	Collection     string // QDRANT_COLLECTION

	ChunkSize    int // CHUNK_SIZE, in characters
	ChunkOverlap int // CHUNK_OVERLAP, in characters
	Workers      int // EMBED_WORKERS: max concurrent embedding batches
	BatchSize    int // EMBED_BATCH_SIZE: chunks per embedding call
	IngestJobs   int // INGEST_WORKERS: documents indexed concurrently in the background
	IngestQueue  int // INGEST_QUEUE: documents that can wait for a worker
	MaxChunks    int // MAX_CHUNKS: larger documents are rejected with 413

	TopKDefault    int     // TOP_K_DEFAULT
	TopKMax        int     // TOP_K_MAX
	ScoreThreshold float64 // SCORE_THRESHOLD: drop hits below this; 0 disables
	RewriteQueries bool    // REWRITE_QUERIES: rewrite follow-ups into standalone queries
	HistoryLen     int     // HISTORY_MESSAGES: messages kept per session

	SessionStore string        // SESSION_STORE: memory or redis
	RedisURL     string        // REDIS_URL, e.g. redis://localhost:6379/0
	SessionTTL   time.Duration // SESSION_TTL: idle sessions are forgotten after this

	MaxUploadBytes int64 // MAX_UPLOAD_MB, stored in bytes

	MaxRetries     int           // LLM_MAX_RETRIES: retries after the first attempt
	RetryBaseDelay time.Duration // LLM_RETRY_BASE_DELAY, e.g. 500ms
	RetryMaxDelay  time.Duration // LLM_RETRY_MAX_DELAY, e.g. 10s
}

// providerDefaults are the models used when CHAT_MODEL / EMBEDDING_MODEL are unset.
var providerDefaults = map[string]struct {
	chat, embedding string
	dim             int
}{
	"gemini": {"gemini-2.5-flash", "gemini-embedding-001", 3072},
	"openai": {"gpt-4o-mini", "text-embedding-3-small", 1536},
}

// Load reads the configuration from the environment and validates it.
func Load() (Config, error) {
	return load(os.Getenv)
}

func load(getenv func(string) string) (Config, error) {
	e := env{get: getenv}
	c := Config{
		Port:      e.str("PORT", "8080"),
		QdrantURL: e.str("QDRANT_URL", "http://localhost:6333"),
		LogFormat: e.str("LOG_FORMAT", "text"),
		LogLevel:  e.str("LOG_LEVEL", "info"),
		Provider:  e.str("LLM_PROVIDER", "gemini"),
	}

	def, ok := providerDefaults[c.Provider]
	if !ok {
		return Config{}, fmt.Errorf("LLM_PROVIDER: unknown provider %q (want gemini or openai)", c.Provider)
	}
	c.ChatModel = e.str("CHAT_MODEL", def.chat)
	c.EmbeddingModel = e.str("EMBEDDING_MODEL", def.embedding)
	c.EmbeddingDim = e.int("EMBEDDING_DIM", def.dim)
	if c.EmbeddingModel != def.embedding && e.get("EMBEDDING_DIM") == "" {
		return Config{}, errors.New("EMBEDDING_DIM must be set when EMBEDDING_MODEL is overridden")
	}
	// One collection per provider by default: their embedding spaces aren't comparable.
	c.Collection = e.str("QDRANT_COLLECTION", "askyourbook_"+c.Provider)

	c.ChunkSize = e.int("CHUNK_SIZE", 1000)
	c.ChunkOverlap = e.int("CHUNK_OVERLAP", 150)
	c.Workers = e.int("EMBED_WORKERS", 4)
	c.BatchSize = e.int("EMBED_BATCH_SIZE", 32)
	c.IngestJobs = e.int("INGEST_WORKERS", 2)
	c.IngestQueue = e.int("INGEST_QUEUE", 16)
	c.MaxChunks = e.int("MAX_CHUNKS", 2000)

	c.TopKDefault = e.int("TOP_K_DEFAULT", 4)
	c.TopKMax = e.int("TOP_K_MAX", 20)
	c.ScoreThreshold = e.float("SCORE_THRESHOLD", 0)
	c.RewriteQueries = e.bool("REWRITE_QUERIES", true)
	c.HistoryLen = e.int("HISTORY_MESSAGES", 10)

	c.SessionStore = e.str("SESSION_STORE", "memory")
	c.RedisURL = e.str("REDIS_URL", "redis://localhost:6379/0")
	c.SessionTTL = e.duration("SESSION_TTL", 24*time.Hour)

	c.MaxUploadBytes = int64(e.int("MAX_UPLOAD_MB", 50)) << 20

	c.MaxRetries = e.int("LLM_MAX_RETRIES", 4)
	c.RetryBaseDelay = e.duration("LLM_RETRY_BASE_DELAY", 500*time.Millisecond)
	c.RetryMaxDelay = e.duration("LLM_RETRY_MAX_DELAY", 10*time.Second)

	if e.err != nil {
		return Config{}, e.err
	}
	return c, c.validate()
}

func (c Config) validate() error {
	var errs []error
	check := func(ok bool, format string, args ...any) {
		if !ok {
			errs = append(errs, fmt.Errorf(format, args...))
		}
	}
	check(c.EmbeddingDim > 0, "EMBEDDING_DIM must be positive")
	check(c.ChunkSize > 0, "CHUNK_SIZE must be positive")
	check(c.ChunkOverlap >= 0 && c.ChunkOverlap < c.ChunkSize, "CHUNK_OVERLAP must be in [0, CHUNK_SIZE)")
	check(c.Workers > 0, "EMBED_WORKERS must be positive")
	check(c.BatchSize > 0, "EMBED_BATCH_SIZE must be positive")
	check(c.IngestJobs > 0, "INGEST_WORKERS must be positive")
	check(c.IngestQueue >= 0, "INGEST_QUEUE must not be negative")
	check(c.MaxChunks > 0, "MAX_CHUNKS must be positive")
	check(c.TopKMax > 0, "TOP_K_MAX must be positive")
	check(c.TopKDefault > 0 && c.TopKDefault <= c.TopKMax, "TOP_K_DEFAULT must be in [1, TOP_K_MAX]")
	check(c.ScoreThreshold >= 0 && c.ScoreThreshold <= 1, "SCORE_THRESHOLD must be in [0, 1]")
	check(c.HistoryLen >= 0, "HISTORY_MESSAGES must not be negative")
	check(c.SessionStore == "memory" || c.SessionStore == "redis", "SESSION_STORE must be memory or redis")
	check(c.SessionTTL > 0, "SESSION_TTL must be positive")
	check(c.MaxUploadBytes > 0, "MAX_UPLOAD_MB must be positive")
	check(c.MaxRetries >= 0, "LLM_MAX_RETRIES must not be negative")
	check(c.RetryBaseDelay > 0 && c.RetryBaseDelay <= c.RetryMaxDelay,
		"LLM_RETRY_BASE_DELAY must be positive and at most LLM_RETRY_MAX_DELAY")
	check(c.LogFormat == "text" || c.LogFormat == "json", "LOG_FORMAT must be text or json")
	return errors.Join(errs...)
}

// env parses typed values, remembering the first parse error.
type env struct {
	get func(string) string
	err error
}

func (e *env) str(key, def string) string {
	if v := e.get(key); v != "" {
		return v
	}
	return def
}

func (e *env) int(key string, def int) int {
	return parse(e, key, def, strconv.Atoi)
}

func (e *env) float(key string, def float64) float64 {
	return parse(e, key, def, func(s string) (float64, error) { return strconv.ParseFloat(s, 64) })
}

func (e *env) bool(key string, def bool) bool {
	return parse(e, key, def, strconv.ParseBool)
}

func (e *env) duration(key string, def time.Duration) time.Duration {
	return parse(e, key, def, time.ParseDuration)
}

func parse[T any](e *env, key string, def T, fn func(string) (T, error)) T {
	v := e.get(key)
	if v == "" {
		return def
	}
	out, err := fn(v)
	if err != nil {
		if e.err == nil {
			e.err = fmt.Errorf("%s: invalid value %q: %w", key, v, err)
		}
		return def
	}
	return out
}
