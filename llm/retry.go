package llm

import (
	"context"
	"math/rand"
	"net/http"
	"time"
)

type RetryPolicy struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
	Factor     float64
	Jitter     float64
	// RetryAfterFunc, when set, returns a custom delay for 429 responses based
	// on the server's Retry-After / rate-limit-reset header. Returning 0 falls
	// back to the exponential backoff below. Set by the client when it parses
	// the header, so a 1-minute TPM window is respected instead of hammering
	// the API with short backoffs.
	RetryAfterFunc func() time.Duration
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries: 5,
		BaseDelay:  1 * time.Second,
		MaxDelay:   60 * time.Second,
		Factor:     2,
		Jitter:     0.25,
	}
}

// RetryDelay computes the wait before a retry. For 429 responses with a known
// Retry-After window it honors the server's window (clamped to MaxDelay); other
// statuses use exponential backoff with jitter.
func (p RetryPolicy) RetryDelay(attempt int, isRateLimit bool) time.Duration {
	if isRateLimit && p.RetryAfterFunc != nil {
		if d := p.RetryAfterFunc(); d > 0 {
			if d > p.MaxDelay {
				return p.MaxDelay
			}
			return d
		}
	}
	return p.backoff(attempt)
}

func (p RetryPolicy) ShouldRetry(status int) bool {
	// Network errors (no HTTP response) are transient and should be retried
	if status == 0 {
		return true
	}
	if status == http.StatusTooManyRequests {
		return true
	}
	return status >= 500 && status <= 599
}

// IsFatal reports whether a status must NOT be retried (persistent errors such
// as insufficient balance/402, invalid request, auth failure). Returning true
// short-circuits the retry loop.
func (p RetryPolicy) IsFatal(status int) bool {
	return status == http.StatusPaymentRequired
}

func (p RetryPolicy) Sleep(ctx context.Context, attempt int) error {
	if attempt <= 0 {
		return nil
	}
	delay := p.backoff(attempt)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p RetryPolicy) SleepFor(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p RetryPolicy) backoff(attempt int) time.Duration {
	multiplier := 1.0
	for i := 1; i < attempt; i++ {
		multiplier *= p.Factor
	}
	delay := time.Duration(float64(p.BaseDelay) * multiplier)
	if delay > p.MaxDelay {
		delay = p.MaxDelay
	}
	if p.Jitter <= 0 {
		return delay
	}
	maxJitter := p.Jitter * float64(delay)
	adjustment := (rand.Float64()*2 - 1) * maxJitter
	return time.Duration(float64(delay) + adjustment)
}
