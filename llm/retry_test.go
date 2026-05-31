package llm

import (
	"context"
	"testing"
	"time"
)

func TestRetryPolicy_ShouldRetry(t *testing.T) {
	p := DefaultRetryPolicy()

	tests := []struct {
		status int
		want   bool
	}{
		{429, true},
		{500, true},
		{502, true},
		{503, true},
		{200, false},
		{400, false},
		{401, false},
		{404, false},
	}

	for _, tt := range tests {
		got := p.ShouldRetry(tt.status)
		if got != tt.want {
			t.Errorf("ShouldRetry(%d) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestRetryPolicy_Backoff_Exponential(t *testing.T) {
	p := RetryPolicy{
		BaseDelay: 100 * time.Millisecond,
		MaxDelay:  10 * time.Second,
		Factor:    2,
		Jitter:    0,
	}

	d1 := p.backoff(1)
	d2 := p.backoff(2)
	d3 := p.backoff(3)

	if d1 != 100*time.Millisecond {
		t.Errorf("backoff(1) = %v, want 100ms", d1)
	}
	if d2 != 200*time.Millisecond {
		t.Errorf("backoff(2) = %v, want 200ms", d2)
	}
	if d3 != 400*time.Millisecond {
		t.Errorf("backoff(3) = %v, want 400ms", d3)
	}
}

func TestRetryPolicy_Backoff_CappedAtMax(t *testing.T) {
	p := RetryPolicy{
		BaseDelay: 1 * time.Second,
		MaxDelay:  5 * time.Second,
		Factor:    10,
		Jitter:    0,
	}

	d := p.backoff(5)
	if d > 5*time.Second {
		t.Errorf("backoff(5) = %v, exceeds max 5s", d)
	}
}

func TestRetryPolicy_Sleep_RespectsContext(t *testing.T) {
	p := RetryPolicy{
		BaseDelay: 10 * time.Second,
		MaxDelay:  30 * time.Second,
		Factor:    2,
		Jitter:    0,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := p.Sleep(ctx, 1)
	elapsed := time.Since(start)

	if err == nil {
		t.Error("expected context error, got nil")
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("Sleep took %v, should have been cancelled quickly", elapsed)
	}
}

func TestRetryPolicy_Sleep_ZeroAttempt(t *testing.T) {
	p := DefaultRetryPolicy()
	err := p.Sleep(context.Background(), 0)
	if err != nil {
		t.Errorf("Sleep(0) = %v, want nil", err)
	}
}
