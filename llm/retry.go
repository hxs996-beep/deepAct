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
}

func DefaultRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries: 3,
		BaseDelay:  1 * time.Second,
		MaxDelay:   30 * time.Second,
		Factor:     2,
		Jitter:     0.25,
	}
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
