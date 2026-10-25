//go:build integration

// Package integration runs the RAG service against a real Qdrant (and,
// optionally, Redis) with a deterministic local embedder, so it needs no
// LLM API key. Start the dependencies with `make up`, then:
//
//	go test -tags integration ./internal/integration
//
// QDRANT_URL (default http://localhost:6333) and TEST_REDIS_URL (optional)
// select the servers. Each run uses, and then deletes, its own collection.
package integration

import (
	"context"
	"errors"
	"hash/fnv"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/ayanshakil/askyourbook/internal/qdrant"
	"github.com/ayanshakil/askyourbook/internal/rag"
	"github.com/ayanshakil/askyourbook/internal/ragtest"
	"github.com/ayanshakil/askyourbook/internal/session"
)

const dim = 256

// bagOfWords embeds text as a normalised histogram of hashed words, so
// chunks sharing words with the question score higher under cosine
// similarity. Deterministic and offline.
type bagOfWords struct{}

func (bagOfWords) EmbedQuery(_ context.Context, text string) ([]float32, error) {
	return embed(text), nil
}

func (bagOfWords) EmbedDocuments(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = embed(t)
	}
	return out, nil
}

func embed(text string) []float32 {
	v := make([]float32, dim)
	words := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	for _, w := range words {
		if len(w) < 3 { // skip "a", "of", "in", ...
			continue
		}
		h := fnv.New32a()
		h.Write([]byte(w))
		v[h.Sum32()%dim]++
	}
	var norm float64
	for _, x := range v {
		norm += float64(x * x)
	}
	if norm > 0 {
		for i := range v {
			v[i] /= float32(math.Sqrt(norm))
		}
	}
	return v
}

func qdrantURL() string {
	if u := os.Getenv("QDRANT_URL"); u != "" {
		return u
	}
	return "http://localhost:6333"
}

// newCollection creates a fresh collection and deletes it after the test.
func newCollection(t *testing.T, qd *qdrant.Client) string {
	t.Helper()
	name := "it_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	if err := qd.EnsureCollection(context.Background(), name, dim); err != nil {
		t.Fatalf("EnsureCollection (is Qdrant running at %s?): %v", qdrantURL(), err)
	}
	t.Cleanup(func() {
		req, _ := http.NewRequest(http.MethodDelete, qdrantURL()+"/collections/"+name, nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
		}
	})
	return name
}

func sessionStore(t *testing.T) rag.SessionStore {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Log("TEST_REDIS_URL not set; using in-memory sessions")
		return session.NewMemory(10, time.Hour)
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	rdb := redis.NewClient(opts)
	t.Cleanup(func() { rdb.Close() })
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Log("using Redis sessions at", opts.Addr)
	return session.NewRedis(rdb, 10, time.Hour)
}

func TestEnsureCollectionIsIdempotentAndChecksSize(t *testing.T) {
	qd := qdrant.New(qdrantURL())
	name := newCollection(t, qd)
	ctx := context.Background()
	if err := qd.EnsureCollection(ctx, name, dim); err != nil {
		t.Fatalf("second EnsureCollection: %v", err)
	}
	err := qd.EnsureCollection(ctx, name, dim*2)
	if err == nil || !strings.Contains(err.Error(), "vector size") {
		t.Fatalf("wrong size: err = %v", err)
	}
}

func TestIngestSearchChatDelete(t *testing.T) {
	qd := qdrant.New(qdrantURL())
	collection := newCollection(t, qd)
	chat := &ragtest.ChatModel{RewriteReply: "Hollin Point Light keeper years"}
	svc := rag.NewService(chat, bagOfWords{}, qd, rag.Options{
		Collection: collection, ChunkSize: 1000, ChunkOverlap: 150,
		Workers: 2, BatchSize: 2, IngestJobs: 1, QueueSize: 2,
		Rewrite: true, Sessions: sessionStore(t),
		Logger: slog.New(slog.DiscardHandler),
	})
	t.Cleanup(func() { svc.Shutdown(context.Background()) })
	ctx := context.Background()

	// Ingest runs in the background; poll until ready.
	f, err := os.Open("../../testdata/sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, _ := f.Stat()
	doc, err := svc.Ingest(ctx, f, st.Size(), "sample.pdf")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for doc.Status != rag.StatusReady {
		if time.Now().After(deadline) {
			t.Fatalf("not ready: %+v", doc)
		}
		time.Sleep(20 * time.Millisecond)
		if doc, err = svc.Document(ctx, doc.ID); err != nil {
			t.Fatal(err)
		}
	}
	if doc.Chunks != 6 || doc.Filename != "sample.pdf" || doc.CreatedAt.IsZero() {
		t.Fatalf("ready doc = %+v", doc)
	}

	// Retrieval through Qdrant's cosine search and doc_id filter finds the
	// page that answers each question, with 1-based page numbers.
	for question, page := range map[string]int{
		"Who rescued eleven sailors from the brig Seraphine?":          2,
		"How long were the Cassel Rock keepers isolated in 1903?":      3,
		"Which lantern was shipped in 214 numbered pieces?":            4,
		"What was the Southreach fog signal nicknamed?":                5,
		"Where is the control room that monitors the lights remotely?": 6,
	} {
		ans, err := svc.Chat(ctx, rag.ChatRequest{DocID: doc.ID, SessionID: uuid.NewString(), Question: question, TopK: 3})
		if err != nil {
			t.Fatal(err)
		}
		if got := ans.Sources[0].Page; got != page {
			t.Errorf("%q: top source is page %d, want %d (sources %+v)", question, got, page, ans.Sources)
		}
	}

	// A follow-up in the same session is rewritten using the stored history.
	sid := uuid.NewString()
	if _, err := svc.Chat(ctx, rag.ChatRequest{DocID: doc.ID, SessionID: sid, Question: "Who kept Hollin Point Light?", TopK: 3}); err != nil {
		t.Fatal(err)
	}
	ans, err := svc.Chat(ctx, rag.ChatRequest{DocID: doc.ID, SessionID: sid, Question: "For how long?", TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if ans.RewrittenQuery == "" || ans.Sources[0].Page != 2 {
		t.Errorf("follow-up: rewritten %q, top page %d", ans.RewrittenQuery, ans.Sources[0].Page)
	}

	// The document is listed, and deleting it removes every point.
	docs, err := svc.ListDocuments(ctx)
	if err != nil || len(docs) != 1 || docs[0].ID != doc.ID || docs[0].Chunks != 6 {
		t.Fatalf("ListDocuments = %+v, %v", docs, err)
	}
	if err := svc.DeleteDocument(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := qd.Count(ctx, collection, qdrant.Match("doc_id", doc.ID)); n != 0 {
		t.Errorf("%d points left after delete", n)
	}
	if _, err := svc.Document(ctx, doc.ID); !errors.Is(err, rag.ErrDocumentNotFound) {
		t.Errorf("Document after delete: %v", err)
	}
	// Qdrant's facet still reports the deleted doc_id with count 0; the
	// list must skip it.
	if docs, _ := svc.ListDocuments(ctx); len(docs) != 0 {
		t.Errorf("ListDocuments after delete = %+v", docs)
	}
}
