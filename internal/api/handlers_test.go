package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ayanshakil/askyourbook/internal/rag"
	"github.com/ayanshakil/askyourbook/internal/ragtest"
)

// readyID is a document already stored in the fake vector store.
const readyID = "11111111-1111-4111-8111-111111111111"

type env struct {
	srv   *httptest.Server
	emb   *ragtest.Embedder
	store *ragtest.Store
	svc   *rag.Service
}

func newEnv(t *testing.T, maxUpload int64) *env {
	t.Helper()
	emb, chat, store := &ragtest.Embedder{}, &ragtest.ChatModel{}, &ragtest.Store{}
	store.Points = ragtest.DocPoints(readyID)
	log := slog.New(slog.DiscardHandler)
	svc := rag.NewService(chat, emb, store, rag.Options{
		Collection: "test", ChunkSize: 1000, ChunkOverlap: 100, Workers: 2, BatchSize: 4,
		IngestJobs: 1, QueueSize: 4, HistoryLen: 10, Logger: log,
	})
	h := NewHandler(svc, Options{MaxUploadBytes: maxUpload, TopKDefault: 4, TopKMax: 20, Logger: log})
	srv := httptest.NewServer(h.Routes())
	t.Cleanup(func() {
		srv.Close()
		svc.Shutdown(context.Background())
	})
	return &env{srv: srv, emb: emb, store: store, svc: svc}
}

func (e *env) do(t *testing.T, method, path, contentType string, body io.Reader) (*http.Response, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, body)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (e *env) postJSON(t *testing.T, path, body string) (*http.Response, []byte) {
	return e.do(t, http.MethodPost, path, "application/json", strings.NewReader(body))
}

// upload posts content as the multipart field "file" (or another field name).
func (e *env) upload(t *testing.T, field, filename string, content []byte) (*http.Response, []byte) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile(field, filename)
	_, _ = fw.Write(content)
	_ = mw.Close()
	return e.do(t, http.MethodPost, "/documents", mw.FormDataContentType(), &buf)
}

func errorOf(t *testing.T, body []byte) string {
	t.Helper()
	var v struct{ Error string }
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("not a JSON error: %s", body)
	}
	return v.Error
}

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// blankPDF returns a valid one-page PDF whose page draws nothing.
func blankPDF() []byte {
	objs := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R >>",
		"<< /Length 0 >>\nstream\n\nendstream",
	}
	var b bytes.Buffer
	b.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objs))
	for i, o := range objs {
		offsets[i] = b.Len()
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", i+1, o)
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(objs)+1)
	for _, off := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objs)+1, xref)
	return b.Bytes()
}

func TestHealthAndRequestID(t *testing.T) {
	e := newEnv(t, 1<<20)
	resp, body := e.do(t, http.MethodGet, "/health", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok"`) {
		t.Fatalf("health = %d %s", resp.StatusCode, body)
	}
	if resp.Header.Get("X-Request-ID") == "" {
		t.Error("no X-Request-ID generated")
	}

	for in, keep := range map[string]bool{"trace-123": true, "bad id with spaces": false} {
		req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/health", nil)
		req.Header.Set("X-Request-ID", in)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if got := resp.Header.Get("X-Request-ID"); (got == in) != keep || got == "" {
			t.Errorf("X-Request-ID %q -> %q (keep=%v)", in, got, keep)
		}
	}
}

func TestUploadValidation(t *testing.T) {
	e := newEnv(t, 4<<10) // 4 KB: sample.pdf fits, the C++ guide doesn't
	tests := []struct {
		name     string
		field    string
		filename string
		content  []byte
		status   int
		errPart  string
	}{
		{"missing file field", "document", "a.pdf", []byte("%PDF-"), 400, `missing "file"`},
		{"wrong extension", "file", "notes.txt", []byte("hello"), 415, "only .pdf"},
		{"not a pdf inside", "file", "fake.pdf", []byte("just text"), 415, "not a PDF"},
		{"empty file", "file", "empty.pdf", nil, 400, "empty"},
		{"unreadable pdf", "file", "broken.pdf", []byte("%PDF-1.4\ngarbage"), 422, "not a readable PDF"},
		{"no text", "file", "blank.pdf", blankPDF(), 422, "no extractable text"},
		{"too large", "file", "guide.pdf", readFile(t, "cpp_programming_guide.pdf"), 413, "upload limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp, body := e.upload(t, tt.field, tt.filename, tt.content)
			if resp.StatusCode != tt.status || !strings.Contains(errorOf(t, body), tt.errPart) {
				t.Fatalf("got %d %s, want %d containing %q", resp.StatusCode, body, tt.status, tt.errPart)
			}
		})
	}

	t.Run("not multipart", func(t *testing.T) {
		resp, _ := e.postJSON(t, "/documents", `{"file":"x"}`)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
}

func TestUploadThenGetDocument(t *testing.T) {
	e := newEnv(t, 1<<20)
	resp, body := e.upload(t, "file", "sample.pdf", readFile(t, "sample.pdf"))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("upload = %d %s", resp.StatusCode, body)
	}
	var up struct {
		DocumentID string `json:"document_id"`
		Status     string
		Chunks     int
	}
	_ = json.Unmarshal(body, &up)
	if up.Status != "processing" || up.Chunks != 6 || resp.Header.Get("Location") != "/documents/"+up.DocumentID {
		t.Fatalf("upload response = %s, Location %q", body, resp.Header.Get("Location"))
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		_, body = e.do(t, http.MethodGet, "/documents/"+up.DocumentID, "", nil)
		if strings.Contains(string(body), `"status":"ready"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("never became ready: %s", body)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDocumentNotFound(t *testing.T) {
	e := newEnv(t, 1<<20)
	for _, path := range []string{"/documents/not-a-uuid", "/documents/22222222-2222-4222-8222-222222222222"} {
		for _, method := range []string{http.MethodGet, http.MethodDelete} {
			if resp, _ := e.do(t, method, path, "", nil); resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s %s = %d, want 404", method, path, resp.StatusCode)
			}
		}
		if resp, _ := e.postJSON(t, path+"/chat", `{"question":"hi"}`); resp.StatusCode != http.StatusNotFound {
			t.Errorf("chat %s = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestChatValidation(t *testing.T) {
	e := newEnv(t, 1<<20)
	path := "/documents/" + readyID + "/chat"
	tests := map[string]struct {
		body    string
		errPart string
	}{
		"bad json":         {`{"question":`, "must be JSON"},
		"missing question": {`{}`, "required"},
		"blank question":   {`{"question":"   "}`, "required"},
		"long question":    {`{"question":"` + strings.Repeat("a", 4001) + `"}`, "at most 4000"},
		"long session id":  {`{"question":"hi","session_id":"` + strings.Repeat("s", 129) + `"}`, "at most 128"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			resp, body := e.postJSON(t, path, tt.body)
			if resp.StatusCode != http.StatusBadRequest || !strings.Contains(errorOf(t, body), tt.errPart) {
				t.Fatalf("got %d %s", resp.StatusCode, body)
			}
		})
	}
}

func TestChatTopK(t *testing.T) {
	e := newEnv(t, 1<<20)
	path := "/documents/" + readyID + "/chat"
	for body, want := range map[string]int{
		`{"question":"q"}`:             4,  // default
		`{"question":"q","top_k":7}`:   7,  // as given
		`{"question":"q","top_k":100}`: 20, // clamped to the max
		`{"question":"q","top_k":-5}`:  1,  // clamped to 1
	} {
		resp, b := e.postJSON(t, path, body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: %d %s", body, resp.StatusCode, b)
		}
		if got := e.store.LastK(); got != want {
			t.Errorf("%s: searched with k=%d, want %d", body, got, want)
		}
	}
}

func TestChatResponse(t *testing.T) {
	e := newEnv(t, 1<<20)
	resp, body := e.postJSON(t, "/documents/"+readyID+"/chat", `{"question":"What joins automatically?"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%d %s", resp.StatusCode, body)
	}
	var out struct {
		SessionID string `json:"session_id"`
		Answer    string
		Sources   []rag.Source
	}
	_ = json.Unmarshal(body, &out)
	if out.SessionID == "" || out.Answer != "the answer" || len(out.Sources) != 2 || out.Sources[0].Page != 3 {
		t.Fatalf("response = %s", body)
	}
	if strings.Contains(string(body), "rewritten_query") {
		t.Error("rewritten_query should be omitted for a first question")
	}
}

func TestChatWhileProcessingIs409(t *testing.T) {
	e := newEnv(t, 1<<20)
	e.emb.Release = make(chan struct{})
	defer close(e.emb.Release)

	_, body := e.upload(t, "file", "sample.pdf", readFile(t, "sample.pdf"))
	var up struct {
		DocumentID string `json:"document_id"`
	}
	_ = json.Unmarshal(body, &up)

	resp, body := e.postJSON(t, "/documents/"+up.DocumentID+"/chat", `{"question":"hi"}`)
	if resp.StatusCode != http.StatusConflict || !strings.Contains(errorOf(t, body), "processing") {
		t.Fatalf("chat = %d %s", resp.StatusCode, body)
	}
	if resp, _ := e.do(t, http.MethodDelete, "/documents/"+up.DocumentID, "", nil); resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete while processing = %d, want 409", resp.StatusCode)
	}
}

func TestChatStream(t *testing.T) {
	e := newEnv(t, 1<<20)
	resp, body := e.postJSON(t, "/documents/"+readyID+"/chat/stream", `{"question":"q","session_id":"s1"}`)
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	events := parseSSE(t, string(body))
	if len(events) != 3 || events[0].name != "token" || events[1].name != "token" || events[2].name != "done" {
		t.Fatalf("events = %+v", events)
	}
	var text string
	for _, ev := range events[:2] {
		var tok struct{ Text string }
		_ = json.Unmarshal([]byte(ev.data), &tok)
		text += tok.Text
	}
	if text != "the answer" {
		t.Errorf("streamed text = %q", text)
	}
	var done struct {
		SessionID string `json:"session_id"`
		Sources   []rag.Source
	}
	_ = json.Unmarshal([]byte(events[2].data), &done)
	if done.SessionID != "s1" || len(done.Sources) != 2 {
		t.Errorf("done = %s", events[2].data)
	}

	// Errors before the first token are ordinary JSON responses.
	resp, body = e.postJSON(t, "/documents/22222222-2222-4222-8222-222222222222/chat/stream", `{"question":"q"}`)
	if resp.StatusCode != http.StatusNotFound || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("unknown doc stream = %d %q %s", resp.StatusCode, resp.Header.Get("Content-Type"), body)
	}
}

type sseEvent struct{ name, data string }

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var out []sseEvent
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		var ev sseEvent
		for _, line := range strings.Split(block, "\n") {
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				ev.name = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok {
				ev.data = v
			}
		}
		out = append(out, ev)
	}
	return out
}

func TestListAndDeleteDocuments(t *testing.T) {
	e := newEnv(t, 1<<20)
	_, body := e.do(t, http.MethodGet, "/documents", "", nil)
	if !strings.Contains(string(body), readyID) {
		t.Fatalf("list = %s", body)
	}
	if resp, _ := e.do(t, http.MethodDelete, "/documents/"+readyID, "", nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	if resp, _ := e.do(t, http.MethodDelete, "/documents/"+readyID, "", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("second delete = %d", resp.StatusCode)
	}
	if _, body := e.do(t, http.MethodGet, "/documents", "", nil); strings.TrimSpace(string(body)) != `{"documents":[]}` {
		t.Fatalf("list after delete = %s", body)
	}
}
