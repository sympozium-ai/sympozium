package webproxy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock returns a nowFunc and an advance function for deterministic time control.
func fakeClock(start time.Time) (func() time.Time, func(d time.Duration)) {
	now := start
	var mu sync.Mutex
	return func() time.Time {
			mu.Lock()
			defer mu.Unlock()
			return now
		}, func(d time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			now = now.Add(d)
		}
}

func TestRateLimiter_Allow_WithinBurst(t *testing.T) {
	rl := NewRateLimiter(60, 5)

	var allowed int
	for i := 0; i < 5; i++ {
		if rl.Allow() {
			allowed++
		}
	}

	if allowed != 5 {
		t.Errorf("expected 5 allowed within burst, got %d", allowed)
	}

	if rl.Allow() {
		t.Error("expected 6th request to be denied")
	}
}

func TestRateLimiter_Allow_Refill(t *testing.T) {
	nowFn, advance := fakeClock(time.Now())
	rl := NewRateLimiter(60, 2) // 1 token/sec, burst 2
	rl.nowFunc = nowFn

	rl.Allow() // 1
	rl.Allow() // 2 — tokens now 0

	if rl.Allow() {
		t.Error("expected 3rd request to be denied before refill")
	}

	advance(1 * time.Second)

	if !rl.Allow() {
		t.Error("expected request to be allowed after 1s refill")
	}

	if rl.Allow() {
		t.Error("expected request to be denied after single refill consumed")
	}
}

func TestRateLimiter_Defaults(t *testing.T) {
	rl := NewRateLimiter(0, 0)

	var allowed int
	for i := 0; i < 10; i++ {
		if rl.Allow() {
			allowed++
		}
	}

	if allowed != 10 {
		t.Errorf("expected 10 allowed with default burst, got %d", allowed)
	}

	if rl.Allow() {
		t.Error("expected 11th request to be denied")
	}
}

func TestRateLimiter_NegativeValues(t *testing.T) {
	rl := NewRateLimiter(-100, -1)

	var allowed int
	for i := 0; i < 10; i++ {
		if rl.Allow() {
			allowed++
		}
	}

	if allowed != 10 {
		t.Errorf("expected 10 allowed with negative inputs (defaults), got %d", allowed)
	}
}

func TestRateLimiter_TokenCap(t *testing.T) {
	nowFn, advance := fakeClock(time.Now())
	rl := NewRateLimiter(60, 3) // 1 token/sec, burst 3
	rl.nowFunc = nowFn

	// Advance well past what would refill more than burst
	advance(10 * time.Second)

	var allowed int
	for i := 0; i < 3; i++ {
		if rl.Allow() {
			allowed++
		}
	}

	if allowed != 3 {
		t.Errorf("expected 3 allowed (capped at burst), got %d", allowed)
	}

	if rl.Allow() {
		t.Error("expected 4th request to be denied")
	}
}

func TestRateLimiter_Concurrent(t *testing.T) {
	rl := NewRateLimiter(1000, 100)

	var allowed atomic.Int64

	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rl.Allow() {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()

	// Burst is 100; a few extra may be allowed due to refill during goroutine scheduling
	got := allowed.Load()
	if got < 95 || got > 115 {
		t.Errorf("expected ~100 allowed concurrently, got %d", got)
	}
}

func TestRateLimiter_HighRPM(t *testing.T) {
	nowFn, advance := fakeClock(time.Now())
	rl := NewRateLimiter(600, 50) // 10 tokens/sec, burst 50
	rl.nowFunc = nowFn

	// Consume all tokens
	for i := 0; i < 50; i++ {
		rl.Allow()
	}

	if rl.Allow() {
		t.Error("expected denial after consuming all tokens")
	}

	// Advance 200ms — should refill ~2 tokens (10/sec * 0.2s)
	advance(200 * time.Millisecond)

	var refilled int
	for i := 0; i < 5; i++ {
		if rl.Allow() {
			refilled++
		}
	}

	if refilled != 2 {
		t.Errorf("expected 2 refilled tokens after 200ms at 10/sec, got %d", refilled)
	}
}

func TestRateLimiter_GradualRefill(t *testing.T) {
	nowFn, advance := fakeClock(time.Now())
	rl := NewRateLimiter(120, 5) // 2 tokens/sec, burst 5
	rl.nowFunc = nowFn

	// Drain all tokens
	for i := 0; i < 5; i++ {
		rl.Allow()
	}

	// Advance 500ms — should refill 1 token (2/sec * 0.5s)
	advance(500 * time.Millisecond)
	if !rl.Allow() {
		t.Error("expected 1 token after 500ms")
	}
	if rl.Allow() {
		t.Error("expected no more tokens after consuming the refilled one")
	}

	// Advance another 1.5s — should refill 3 tokens (2/sec * 1.5s)
	advance(1500 * time.Millisecond)
	var allowed int
	for i := 0; i < 5; i++ {
		if rl.Allow() {
			allowed++
		}
	}
	if allowed != 3 {
		t.Errorf("expected 3 tokens after 1.5s, got %d", allowed)
	}
}
