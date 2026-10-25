package retry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"
)

// httpErr mimics *apierror.APIError from the Google client.
type httpErr struct{ code int }

func (e httpErr) Error() string { return fmt.Sprintf("googleapi: Error %d", e.code) }
func (e httpErr) HTTPCode() int { return e.code }

func TestRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"google 429", fmt.Errorf("embed: %w", httpErr{429}), true},
		{"google 503", httpErr{503}, true},
		{"google 400", httpErr{400}, false},
		{"google 401", httpErr{401}, false},
		{"openai 429", errors.New("API returned unexpected status code: 429: Rate limit reached"), true},
		{"openai 500", errors.New("API returned unexpected status code: 500: oops"), true},
		{"openai 400", errors.New("API returned unexpected status code: 400: bad"), false},
		{"network", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"canceled", fmt.Errorf("x: %w", context.Canceled), false},
		{"deadline", context.DeadlineExceeded, false},
		{"plain", errors.New("something else"), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		if got := Retryable(tt.err); got != tt.want {
			t.Errorf("%s: Retryable = %v, want %v", tt.name, got, tt.want)
		}
	}
}

var fast = Policy{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond}

func TestDoRetriesThenSucceeds(t *testing.T) {
	calls, retries := 0, 0
	err := Do(context.Background(), fast, func(int, time.Duration, error) { retries++ }, func() error {
		calls++
		if calls < 3 {
			return httpErr{429}
		}
		return nil
	})
	if err != nil || calls != 3 || retries != 2 {
		t.Fatalf("err=%v calls=%d retries=%d", err, calls, retries)
	}
}

func TestDoGivesUpAfterMaxRetries(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fast, nil, func() error { calls++; return httpErr{503} })
	if calls != 4 || err == nil { // 1 attempt + 3 retries
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDoStopsOnPermanentError(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fast, nil, func() error { calls++; return httpErr{400} })
	if calls != 1 || err == nil {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestDoStopsWhenContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := Policy{MaxRetries: 5, BaseDelay: time.Hour, MaxDelay: time.Hour}
	calls := 0
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	err := Do(ctx, p, nil, func() error { calls++; return httpErr{429} })
	if calls != 1 || !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestBackoffBounds(t *testing.T) {
	p := Policy{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}
	for attempt, ceiling := range []time.Duration{100, 200, 400, 800, 1000, 1000} {
		ceiling *= time.Millisecond
		for range 100 {
			if d := p.backoff(attempt); d < 0 || d > ceiling {
				t.Fatalf("attempt %d: backoff %v outside [0, %v]", attempt, d, ceiling)
			}
		}
	}
	if d := p.backoff(100); d > time.Second {
		t.Fatalf("large attempt: backoff %v exceeds MaxDelay", d)
	}
}
