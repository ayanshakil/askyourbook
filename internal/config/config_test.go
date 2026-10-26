package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// envOf returns a getenv over m. Unless m says otherwise it includes a
// Gemini key, which every valid configuration needs.
func envOf(m map[string]string) func(string) string {
	return func(k string) string {
		if k == "GOOGLE_API_KEY" {
			if v, ok := m[k]; ok {
				return v
			}
			if m["OPENAI_API_KEY"] == "" && m["GEMINI_API_KEY"] == "" && m["LLM_PROVIDER"] != "openai" {
				return "test-google-key"
			}
			return ""
		}
		return m[k]
	}
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

func TestProviderChosenFromKeys(t *testing.T) {
	tests := []struct {
		name             string
		env              map[string]string
		provider, source string
		key, embedding   string
		collection       string
	}{
		{"only a Gemini key", map[string]string{"GOOGLE_API_KEY": "g"},
			"gemini", "GOOGLE_API_KEY", "g", "gemini-embedding-001", "askyourbook_gemini"},
		{"GEMINI_API_KEY also works", map[string]string{"GOOGLE_API_KEY": "", "GEMINI_API_KEY": "g2"},
			"gemini", "GEMINI_API_KEY", "g2", "gemini-embedding-001", "askyourbook_gemini"},
		{"only an OpenAI key", map[string]string{"GOOGLE_API_KEY": "", "OPENAI_API_KEY": "sk"},
			"openai", "OPENAI_API_KEY", "sk", "text-embedding-3-small", "askyourbook_openai"},
		{"both keys: Gemini", map[string]string{"GOOGLE_API_KEY": "g", "OPENAI_API_KEY": "sk"},
			"gemini", "GOOGLE_API_KEY", "g", "gemini-embedding-001", "askyourbook_gemini"},
		{"both keys, LLM_PROVIDER=openai", map[string]string{"GOOGLE_API_KEY": "g", "OPENAI_API_KEY": "sk", "LLM_PROVIDER": "openai"},
			"openai", "LLM_PROVIDER", "sk", "text-embedding-3-small", "askyourbook_openai"},
		{"LLM_PROVIDER is case-insensitive", map[string]string{"OPENAI_API_KEY": "sk", "LLM_PROVIDER": " OpenAI "},
			"openai", "LLM_PROVIDER", "sk", "text-embedding-3-small", "askyourbook_openai"},
		{"surrounding spaces in a key are trimmed", map[string]string{"GOOGLE_API_KEY": "", "OPENAI_API_KEY": "  sk  "},
			"openai", "OPENAI_API_KEY", "sk", "text-embedding-3-small", "askyourbook_openai"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := load(envOf(tt.env))
			if err != nil {
				t.Fatal(err)
			}
			if c.Provider != tt.provider || c.ProviderSource != tt.source || string(c.APIKey) != tt.key ||
				c.EmbeddingModel != tt.embedding || c.Collection != tt.collection {
				t.Errorf("got provider=%s source=%s key=%q embedding=%s collection=%s",
					c.Provider, c.ProviderSource, string(c.APIKey), c.EmbeddingModel, c.Collection)
			}
		})
	}
}

func TestAPIKeyIsRedactedWhenPrinted(t *testing.T) {
	c, err := load(envOf(map[string]string{"GOOGLE_API_KEY": "super-secret"}))
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
		if out := fmt.Sprintf(format, c); strings.Contains(out, "super-secret") {
			t.Errorf("%s leaks the key: %s", format, out)
		}
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
	noKeys := map[string]string{"GOOGLE_API_KEY": ""}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{"GOOGLE_API_KEY": ""}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}
	tests := map[string]struct {
		env  map[string]string
		want string
	}{
		"no key at all":       {noKeys, "no LLM API key found"},
		"typo in key name":    {with(map[string]string{"OPEN_API_KEY": "sk"}), "did you mean OPENAI_API_KEY"},
		"openai without key":  {with(map[string]string{"LLM_PROVIDER": "openai", "GEMINI_API_KEY": "g"}), "needs OPENAI_API_KEY"},
		"gemini without key":  {with(map[string]string{"LLM_PROVIDER": "gemini", "OPENAI_API_KEY": "sk"}), "needs GOOGLE_API_KEY"},
		"unknown provider":    {map[string]string{"LLM_PROVIDER": "llama", "GOOGLE_API_KEY": "g"}, "unknown provider"},
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
