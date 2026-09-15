package llm

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

// AdaptiveLimiter bounds concurrent LLM requests. Concurrency is governed by
// currentSlots — a soft, adaptive target: Record429 halves it after three
// consecutive rate-limit responses, RecordSuccess grows it back toward maxSlots.
// The hard ceiling is maxSlots.
//
// Unlike the old implementation, the semaphore instance is never replaced:
// in-flight tracking is an atomic counter gated against currentSlots, so there
// is no race between Acquire reading a semaphore pointer and Record*/replacing
// it, and released slots can never be lost to a stale instance.
type AdaptiveLimiter struct {
	// inFlight counts requests that have passed the gate (atomic).
	inFlight atomic.Int64
	// currentSlots is the soft concurrency target, mutated under mu,
	// read atomically by Acquire.
	currentSlots atomic.Int64
	rateLimiter  *rate.Limiter
	maxSlots     int64
	minSlots     int64
	mu           sync.Mutex
	consecutive  int
	lastSuccess  time.Time
	last429      time.Time
	// wake is a buffered signal (capacity 1) that a slot was released.
	// The buffer prevents lost wakeups: a release that fires before a waiter
	// enters its select still delivers its token.
	wake chan struct{}
}

func NewAdaptiveLimiter(initialSlots int64, maxSlots int64, minSlots int64, rps rate.Limit, burst int) *AdaptiveLimiter {
	if initialSlots <= 0 {
		initialSlots = 1
	}
	if maxSlots < initialSlots {
		maxSlots = initialSlots
	}
	if minSlots <= 0 {
		minSlots = 1
	}
	if minSlots > maxSlots {
		minSlots = maxSlots
	}
	l := &AdaptiveLimiter{
		rateLimiter: rate.NewLimiter(rps, burst),
		maxSlots:    maxSlots,
		minSlots:    minSlots,
		wake:        make(chan struct{}, 1),
	}
	l.currentSlots.Store(initialSlots)
	return l
}

func (l *AdaptiveLimiter) Acquire(ctx context.Context) error {
	if err := l.rateLimiter.Wait(ctx); err != nil {
		return err
	}
	for {
		if l.tryAcquire() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-l.wake:
		}
	}
}

// tryAcquire attempts to claim one slot under the current target. Returns true
// on success; false when the target is reached or a race was lost (in which
// case the caller waits for a release).
func (l *AdaptiveLimiter) tryAcquire() bool {
	cur := l.currentSlots.Load()
	if l.inFlight.Load() >= cur {
		return false
	}
	if l.inFlight.Add(1) <= cur {
		return true
	}
	l.inFlight.Add(-1)
	return false
}

func (l *AdaptiveLimiter) Release() {
	l.inFlight.Add(-1)
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *AdaptiveLimiter) Record429() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.consecutive++
	l.last429 = time.Now()
	if l.consecutive < 3 {
		return
	}
	newSlots := l.currentSlots.Load() / 2
	if newSlots < l.minSlots {
		newSlots = l.minSlots
	}
	if newSlots != l.currentSlots.Load() {
		l.currentSlots.Store(newSlots)
	}
	l.consecutive = 0
}

func (l *AdaptiveLimiter) RecordSuccess() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if !l.lastSuccess.IsZero() && now.Sub(l.lastSuccess) < 60*time.Second {
		return
	}
	l.lastSuccess = now
	if l.currentSlots.Load() >= l.maxSlots {
		return
	}
	newSlots := l.currentSlots.Load() + 1
	if newSlots > l.maxSlots {
		newSlots = l.maxSlots
	}
	if newSlots != l.currentSlots.Load() {
		l.currentSlots.Store(newSlots)
	}
	if l.consecutive > 0 {
		l.consecutive = 0
	}
}

func (l *AdaptiveLimiter) Slots() int64 {
	return l.currentSlots.Load()
}
