package llm

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAdaptiveLimiter_AcquireRelease(t *testing.T) {
	lim := NewAdaptiveLimiter(3, 10, 1, 100, 10)

	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := lim.Acquire(ctx); err != nil {
			t.Fatalf("Acquire %d: %v", i, err)
		}
	}

	done := make(chan struct{})
	go func() {
		ctx2, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		defer cancel()
		err := lim.Acquire(ctx2)
		if err == nil {
			t.Error("expected timeout, got nil")
		}
		close(done)
	}()
	<-done

	lim.Release()
	if err := lim.Acquire(ctx); err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}
	lim.Release()
	lim.Release()
	lim.Release()
}

func TestAdaptiveLimiter_Record429_HalvesAfterThree(t *testing.T) {
	lim := NewAdaptiveLimiter(8, 10, 1, 100, 10)

	lim.Record429()
	lim.Record429()
	if lim.Slots() != 8 {
		t.Errorf("after 2x 429: slots = %d, want 8", lim.Slots())
	}

	lim.Record429()
	if lim.Slots() != 4 {
		t.Errorf("after 3x 429: slots = %d, want 4", lim.Slots())
	}
}

func TestAdaptiveLimiter_Record429_RespectsMinSlots(t *testing.T) {
	lim := NewAdaptiveLimiter(2, 10, 1, 100, 10)

	lim.Record429()
	lim.Record429()
	lim.Record429()
	if lim.Slots() != 1 {
		t.Errorf("slots = %d, want 1 (min)", lim.Slots())
	}

	lim.Record429()
	lim.Record429()
	lim.Record429()
	if lim.Slots() != 1 {
		t.Errorf("slots = %d, want 1 (should not go below min)", lim.Slots())
	}
}

func TestAdaptiveLimiter_RecordSuccess_Increments(t *testing.T) {
	lim := NewAdaptiveLimiter(3, 5, 1, 100, 10)

	lim.RecordSuccess()
	if lim.Slots() != 4 {
		t.Errorf("after first success: slots = %d, want 4", lim.Slots())
	}
}

func TestAdaptiveLimiter_RecordSuccess_RespectsMaxSlots(t *testing.T) {
	lim := NewAdaptiveLimiter(5, 5, 1, 100, 10)

	lim.RecordSuccess()
	if lim.Slots() != 5 {
		t.Errorf("slots = %d, want 5 (max)", lim.Slots())
	}
}

func TestAdaptiveLimiter_ConcurrentAccess(t *testing.T) {
	lim := NewAdaptiveLimiter(10, 10, 1, 1000, 20)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := context.Background()
			if err := lim.Acquire(ctx); err != nil {
				return
			}
			time.Sleep(5 * time.Millisecond)
			lim.Release()
		}()
	}
	wg.Wait()
}
