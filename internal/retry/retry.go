// Package retry retries transient LLM provider failures (HTTP 429, 5xx and
// network errors) with exponential backoff and full jitter.
package retry

import (
	"context"
	"errors"
	"math/rand/v2"
	"net"
	"regexp"
	"strconv"
	"time"
)

// Policy bounds the retries. Zero MaxRetries disables retrying.
type Policy struct {
	MaxRetries int           // retries after the first attempt
	BaseDelay  time.Duration // cap of the first backoff; doubles per attempt
	MaxDelay   time.Duration // cap of any single backoff
}

// Do calls fn until it succeeds, returns a non-retryable error, the retries
// run out, or ctx is done. onRetry, if non-nil, is called before each wait.
func Do(ctx context.Context, p Policy, onRetry func(attempt int, wait time.Duration, err error), fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil || attempt >= p.MaxRetries || !Retryable(err) {
			return err
		}
		wait := p.backoff(attempt)
		if onRetry != nil {
			onRetry(attempt+1, wait, err)
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return errors.Join(err, ctx.Err())
		case <-t.C:
		}
	}
}

// backoff returns a random wait in [0, min(MaxDelay, BaseDelay*2^attempt)]
// ("full jitter"), which spreads out retries from concurrent workers.
func (p Policy) backoff(attempt int) time.Duration {
	ceiling := p.MaxDelay
	if attempt < 32 { // avoid overflowing the shift
		ceiling = min(p.MaxDelay, p.BaseDelay<<attempt)
	}
	if ceiling <= 0 {
		return 0
	}
	return rand.N(ceiling + 1)
}

// statusInMessage finds an HTTP status in error text, as produced by the
// openai client ("API returned unexpected status code: 429: ...").
var statusInMessage = regexp.MustCompile(`status code: (\d{3})`)

// Retryable reports whether err is worth retrying: rate limiting (429),
// server errors (5xx) and network failures. Cancellation is never retried.
func Retryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if code, ok := StatusCode(err); ok {
		return code == 429 || code >= 500
	}
	var netErr net.Error
	return errors.As(err, &netErr)
}

// StatusCode extracts the HTTP status of a provider error, if it has one.
func StatusCode(err error) (int, bool) {
	// Google API errors (googleai): *apierror.APIError.
	var h interface{ HTTPCode() int }
	if errors.As(err, &h) && h.HTTPCode() > 0 {
		return h.HTTPCode(), true
	}
	if m := statusInMessage.FindStringSubmatch(err.Error()); m != nil {
		code, _ := strconv.Atoi(m[1])
		return code, true
	}
	return 0, false
}
