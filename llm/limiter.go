package llm

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"
	"golang.org/x/time/rate"
)

type AdaptiveLimiter struct {
	sem          *semaphore.Weighted
	rateLimiter  *rate.Limiter
	maxSlots     int64
	minSlots     int64
	mu           sync.Mutex
	currentSlots int64
	consecutive  int
	lastSuccess  time.Time
	last429      time.Time
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
	return &AdaptiveLimiter{
		sem:          semaphore.NewWeighted(initialSlots),
		rateLimiter:  rate.NewLimiter(rps, burst),
		maxSlots:     maxSlots,
		minSlots:     minSlots,
		currentSlots: initialSlots,
	}
}

func (l *AdaptiveLimiter) Acquire(ctx context.Context) error {
	if err := l.rateLimiter.Wait(ctx); err != nil {
		return err
	}
	return l.sem.Acquire(ctx, 1)
}

func (l *AdaptiveLimiter) Release() {
	l.mu.Lock()
	sem := l.sem
	l.mu.Unlock()
	if sem == nil {
		return
	}
	defer func() {
		if recover() != nil {
			return
		}
	}()
	sem.Release(1)
}

func (l *AdaptiveLimiter) Record429() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.consecutive++
	l.last429 = time.Now()
	if l.consecutive < 3 {
		return
	}
	newSlots := l.currentSlots / 2
	if newSlots < l.minSlots {
		newSlots = l.minSlots
	}
	if newSlots != l.currentSlots {
		l.currentSlots = newSlots
		l.sem = semaphore.NewWeighted(newSlots)
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
	if l.currentSlots >= l.maxSlots {
		return
	}
	newSlots := l.currentSlots + 1
	if newSlots > l.maxSlots {
		newSlots = l.maxSlots
	}
	if newSlots != l.currentSlots {
		l.currentSlots = newSlots
		l.sem = semaphore.NewWeighted(newSlots)
	}
	if l.consecutive > 0 {
		l.consecutive = 0
	}
}

func (l *AdaptiveLimiter) Slots() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.currentSlots
}
