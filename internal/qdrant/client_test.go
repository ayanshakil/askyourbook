package qdrant

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// request is what the fake server received.
type request struct {
	Method string
	Path   string // including the query string
	Body   map[string]any
}

// fakeQdrant answers "METHOD /path?query" with the matching canned response
// (status and JSON body) and records every request.
type fakeQdrant struct {
	mu        sync.Mutex
	requests  []request
	responses map[string]response
}

type response struct {
	status int
	body   string
}

func newFakeQdrant(t *testing.T, responses map[string]response) (*fakeQdrant, *Client) {
	f := &fakeQdrant{responses: responses}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, New(srv.URL)
}

func (f *fakeQdrant) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		_ = json.Unmarshal(b, &body)
	}
	key := r.Method + " " + r.URL.RequestURI()
	f.mu.Lock()
	f.requests = append(f.requests, request{r.Method, r.URL.RequestURI(), body})
	resp, ok := f.responses[key]
	f.mu.Unlock()
	if !ok {
		resp = response{http.StatusInternalServerError, `{"status":{"error":"unexpected request ` + key + `"}}`}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.status)
	_, _ = io.WriteString(w, resp.body)
}

func (f *fakeQdrant) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.requests {
		out = append(out, r.Method+" "+r.Path)
	}
	return out
}

func (f *fakeQdrant) last() request {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

var ok = response{http.StatusOK, `{"result":true,"status":"ok"}`}

func collectionInfo(size int) response {
	return response{http.StatusOK,
		fmt.Sprintf(`{"result":{"config":{"params":{"vectors":{"size":%d,"distance":"Cosine"}}}}}`, size)}
}

func TestEnsureCollectionCreatesOn404(t *testing.T) {
	f, c := newFakeQdrant(t, map[string]response{
		"GET /collections/books":                 {http.StatusNotFound, `{"status":{"error":"Not found"}}`},
		"PUT /collections/books":                 ok,
		"PUT /collections/books/index?wait=true": ok,
	})
	if err := c.EnsureCollection(context.Background(), "books", 3072); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /collections/books", "PUT /collections/books", "PUT /collections/books/index?wait=true"}
	if got := f.paths(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	create := f.requests[1].Body["vectors"].(map[string]any)
	if create["size"] != float64(3072) || create["distance"] != "Cosine" {
		t.Errorf("create body = %v", create)
	}
	if idx := f.requests[2].Body; idx["field_name"] != "doc_id" || idx["field_schema"] != "keyword" {
		t.Errorf("index body = %v", idx)
	}
}

func TestEnsureCollectionAcceptsMatchingSize(t *testing.T) {
	f, c := newFakeQdrant(t, map[string]response{
		"GET /collections/books":                 collectionInfo(3072),
		"PUT /collections/books/index?wait=true": ok,
	})
	if err := c.EnsureCollection(context.Background(), "books", 3072); err != nil {
		t.Fatal(err)
	}
	for _, p := range f.paths() {
		if p == "PUT /collections/books" {
			t.Fatal("recreated an existing collection")
		}
	}
}

func TestEnsureCollectionRejectsWrongSize(t *testing.T) {
	_, c := newFakeQdrant(t, map[string]response{"GET /collections/books": collectionInfo(1536)})
	err := c.EnsureCollection(context.Background(), "books", 3072)
	if err == nil || !strings.Contains(err.Error(), "vector size 1536") || !strings.Contains(err.Error(), "3072") {
		t.Fatalf("err = %v", err)
	}
}

func TestEnsureCollectionFailsOnOtherStatus(t *testing.T) {
	f, c := newFakeQdrant(t, map[string]response{
		"GET /collections/books": {http.StatusInternalServerError, `{"status":{"error":"disk full"}}`},
	})
	err := c.EnsureCollection(context.Background(), "books", 3072)
	if err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("err = %v", err)
	}
	if len(f.paths()) != 1 {
		t.Fatalf("made more requests after a 500: %v", f.paths())
	}
}

func TestSearch(t *testing.T) {
	f, c := newFakeQdrant(t, map[string]response{
		"POST /collections/books/points/query": {http.StatusOK, `{"result":{"points":[
			{"id":"a","score":0.82,"payload":{"page":3,"text":"hello"}},
			{"id":"b","score":0.61,"payload":{"page":7,"text":"world"}}]}}`},
	})
	hits, err := c.Search(context.Background(), "books", []float32{0.5, 0.25}, 2, Match("doc_id", "d1"))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || hits[0].Score != 0.82 || hits[1].Payload["text"] != "world" {
		t.Fatalf("hits = %+v", hits)
	}
	body := f.last().Body
	if body["limit"] != float64(2) || body["with_payload"] != true {
		t.Errorf("body = %v", body)
	}
	if q := body["query"].([]any); len(q) != 2 || q[0] != 0.5 {
		t.Errorf("query vector = %v", q)
	}
	must := body["filter"].(map[string]any)["must"].([]any)[0].(map[string]any)
	if must["key"] != "doc_id" || must["match"].(map[string]any)["value"] != "d1" {
		t.Errorf("filter = %v", must)
	}
}

func TestUpsertCountScrollFacetDelete(t *testing.T) {
	f, c := newFakeQdrant(t, map[string]response{
		"PUT /collections/books/points?wait=true":         ok,
		"POST /collections/books/points/count":            {http.StatusOK, `{"result":{"count":42}}`},
		"POST /collections/books/points/scroll":           {http.StatusOK, `{"result":{"points":[{"id":"a","payload":{"filename":"x.pdf"}}]}}`},
		"POST /collections/books/facet":                   {http.StatusOK, `{"result":{"hits":[{"value":"d1","count":5},{"value":"d2","count":0}]}}`},
		"POST /collections/books/points/delete?wait=true": ok,
	})
	ctx := context.Background()

	pts := []Point{{ID: "p1", Vector: []float32{1}, Payload: map[string]any{"doc_id": "d1"}}}
	if err := c.Upsert(ctx, "books", pts); err != nil {
		t.Fatal(err)
	}
	if got := f.last().Body["points"].([]any)[0].(map[string]any); got["id"] != "p1" {
		t.Errorf("upsert body = %v", got)
	}

	if n, err := c.Count(ctx, "books", Match("doc_id", "d1")); err != nil || n != 42 {
		t.Errorf("Count = %d, %v", n, err)
	}
	if f.last().Body["exact"] != true {
		t.Error("count is not exact")
	}

	recs, err := c.Scroll(ctx, "books", Match("doc_id", "d1"), 1, "filename")
	if err != nil || len(recs) != 1 || recs[0].Payload["filename"] != "x.pdf" {
		t.Errorf("Scroll = %+v, %v", recs, err)
	}
	if b := f.last().Body; b["with_vector"] != false || b["with_payload"].([]any)[0] != "filename" {
		t.Errorf("scroll body = %v", b)
	}

	hits, err := c.Facet(ctx, "books", "doc_id", 10)
	if err != nil || len(hits) != 2 || hits[0] != (FacetHit{"d1", 5}) || hits[1].Count != 0 {
		t.Errorf("Facet = %+v, %v", hits, err)
	}

	if err := c.Delete(ctx, "books", Match("doc_id", "d1")); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.last().Body["filter"]; !ok {
		t.Errorf("delete body = %v", f.last().Body)
	}
}

func TestErrorsIncludeStatusAndBody(t *testing.T) {
	_, c := newFakeQdrant(t, map[string]response{
		"POST /collections/books/points/count": {http.StatusBadRequest, `{"status":{"error":"bad filter"}}`},
	})
	_, err := c.Count(context.Background(), "books", Match("doc_id", "d1"))
	if err == nil || !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "bad filter") ||
		!strings.Contains(err.Error(), "/points/count") {
		t.Fatalf("err = %v", err)
	}
}

func TestContextCancellation(t *testing.T) {
	_, c := newFakeQdrant(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Count(ctx, "books", nil); err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("err = %v", err)
	}
}
