package retry

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tmc/langchaingo/embeddings"
	"github.com/tmc/langchaingo/llms/googleai"
)

// statusTransport answers every request with the next status in the list
// (the last one repeats), without touching the network.
type statusTransport struct {
	statuses []int
	calls    atomic.Int32
}

func (t *statusTransport) RoundTrip(*http.Request) (*http.Response, error) {
	i := int(t.calls.Add(1)) - 1
	code := t.statuses[min(i, len(t.statuses)-1)]
	body := `{"error":{"code":` + http.StatusText(code) + `}}`
	if code == http.StatusOK {
		body = `{"embeddings":[{"values":[0.1,0.2,0.3]}]}` // batchEmbedContents

	}
	return &http.Response{
		StatusCode: code,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

// TestGoogleAIErrorsAreClassified checks the real googleai client's error
// wrapping end to end: 429 and 503 are retried, 400 is not.
func TestGoogleAIErrorsAreClassified(t *testing.T) {
	tests := []struct {
		name      string
		statuses  []int
		wantCalls int32
		wantErr   bool
	}{
		{"429 then 503 then ok", []int{429, 503, 200}, 3, false},
		{"400 is permanent", []int{400}, 1, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := &statusTransport{statuses: tt.statuses}
			llm, err := googleai.New(context.Background(),
				googleai.WithAPIKey("test"),
				googleai.WithHTTPClient(&http.Client{Transport: tr}))
			if err != nil {
				t.Fatal(err)
			}
			emb, err := embeddings.NewEmbedder(llm)
			if err != nil {
				t.Fatal(err)
			}
			p := Policy{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
			r := NewEmbedder(emb, p, slog.New(slog.DiscardHandler))

			_, err = r.EmbedQuery(context.Background(), "hello")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got := tr.calls.Load(); got != tt.wantCalls {
				t.Fatalf("calls = %d, want %d (err: %v)", got, tt.wantCalls, err)
			}
		})
	}
}
