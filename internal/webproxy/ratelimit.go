// Package webproxy provides the HTTP proxy that exposes Sympozium agents
// as OpenAI-compatible and MCP endpoints.
package webproxy

import (
	"sync"
	"time"
)

// RateLimiter implements a token bucket rate limiter.
type RateLimiter struct {
	mu         sync.Mutex
	tokens     float64
	maxTokens  float64
	refillRate float64 // tokens per second
	lastRefill time.Time
	nowFunc    func() time.Time // injectable clock for testing
}

// NewRateLimiter creates a rate limiter with the given requests per minute and burst size.
func NewRateLimiter(requestsPerMinute, burstSize int) *RateLimiter {
	if requestsPerMinute <= 0 {
		requestsPerMinute = 60
	}
	if burstSize <= 0 {
		burstSize = 10
	}
	return &RateLimiter{
		tokens:     float64(burstSize),
		maxTokens:  float64(burstSize),
		refillRate: float64(requestsPerMinute) / 60.0,
		lastRefill: time.Now(),
	}
}

// Allow returns true if the request is allowed under the rate limit.
func (rl *RateLimiter) Allow() bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := rl.now()
	elapsed := now.Sub(rl.lastRefill).Seconds()
	rl.tokens += elapsed * rl.refillRate
	if rl.tokens > rl.maxTokens {
		rl.tokens = rl.maxTokens
	}
	rl.lastRefill = now

	if rl.tokens >= 1 {
		rl.tokens--
		return true
	}
	return false
}

func (rl *RateLimiter) now() time.Time {
	if rl.nowFunc != nil {
		return rl.nowFunc()
	}
	return time.Now()
}
