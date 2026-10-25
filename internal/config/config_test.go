package config

import (
	"strings"
	"testing"
	"time"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	c, err := load(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.Provider != "gemini" || c.ChatModel != "gemini-2.5-flash" || c.EmbeddingDim != 3072 {
		t.Errorf("provider defaults = %q %q %d", c.Provider, c.ChatModel, c.EmbeddingDim)
	}
	if c.Collection != "askyourbook_gemini" {
		t.Errorf("Collection = %q", c.Collection)
	}
	if c.MaxUploadBytes != 50<<20 || c.TopKDefault != 4 || c.TopKMax != 20 || c.Workers != 4 {
		t.Errorf("unexpected defaults: %+v", c)
	}
}

func TestLoadOpenAI(t *testing.T) {
	c, err := load(envOf(map[string]string{"LLM_PROVIDER": "openai"}))
	if err != nil {
		t.Fatal(err)
	}
	if c.EmbeddingModel != "text-embedding-3-small" || c.EmbeddingDim != 1536 || c.Collection != "askyourbook_openai" {
		t.Errorf("openai defaults = %q %d %q", c.EmbeddingModel, c.EmbeddingDim, c.Collection)
	}
}

func TestLoadOverrides(t *testing.T) {
	c, err := load(envOf(map[string]string{
		"EMBED_WORKERS":        "8",
		"SCORE_THRESHOLD":      "0.65",
		"REWRITE_QUERIES":      "false",
		"LLM_RETRY_BASE_DELAY": "250ms",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Workers != 8 || c.ScoreThreshold != 0.65 || c.RewriteQueries || c.RetryBaseDelay != 250*time.Millisecond {
		t.Errorf("overrides not applied: %+v", c)
	}
}

func TestLoadErrors(t *testing.T) {
	tests := map[string]struct {
		env  map[string]string
		want string
	}{
		"unknown provider":    {map[string]string{"LLM_PROVIDER": "llama"}, "unknown provider"},
		"bad int":             {map[string]string{"EMBED_WORKERS": "four"}, "EMBED_WORKERS"},
		"overlap too big":     {map[string]string{"CHUNK_SIZE": "100", "CHUNK_OVERLAP": "100"}, "CHUNK_OVERLAP"},
		"default above max":   {map[string]string{"TOP_K_DEFAULT": "30"}, "TOP_K_DEFAULT"},
		"model without dim":   {map[string]string{"EMBEDDING_MODEL": "other"}, "EMBEDDING_DIM"},
		"threshold above one": {map[string]string{"SCORE_THRESHOLD": "1.5"}, "SCORE_THRESHOLD"},
		"unknown store":       {map[string]string{"SESSION_STORE": "postgres"}, "SESSION_STORE"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := load(envOf(tt.env))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}
