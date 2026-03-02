// ratelimit.go implements token-bucket rate limiting for the Polymarket US API.
//
// Polymarket US enforces per-category rate limits measured in requests per
// 10-second windows. This file provides a smooth token-bucket implementation
// that refills continuously (rather than in 10s bursts) to avoid hard limits.
//
// Published limits (per 10 seconds, per IP):
//   - Global:  2,000 requests across all endpoints
//   - Order:    400 (POST /v1/orders, DELETE /v1/order/{id}/cancel)
//   - Book:      50 (GET /v1/orders/open)  — most conservative read endpoint
//
// We expose three buckets that cover the common trading operations:
//   - Global: 200 burst / 200 per sec (maps to 2000/10s global limit)
//   - Order:   40 burst /  40 per sec (maps to 400/10s order limit)
//   - Book:     5 burst /   5 per sec (maps to 50/10s read limit)
package exchange

import (
	"context"
	"sync"
	"time"
)

// TokenBucket implements a token-bucket rate limiter with continuous refill.
// Callers block in Wait() until a token is available or the context is cancelled.
type TokenBucket struct {
	mu       sync.Mutex
	tokens   float64   // current available tokens (fractional allowed)
	capacity float64   // maximum burst size
	rate     float64   // tokens refilled per second
	lastTime time.Time // last time tokens were calculated
}

// NewTokenBucket creates a rate limiter with the given capacity and refill rate.
func NewTokenBucket(capacity, ratePerSecond float64) *TokenBucket {
	return &TokenBucket{
		tokens:   capacity,
		capacity: capacity,
		rate:     ratePerSecond,
		lastTime: time.Now(),
	}
}

// Wait blocks until a token is available or ctx is cancelled.
func (tb *TokenBucket) Wait(ctx context.Context) error {
	for {
		tb.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(tb.lastTime).Seconds()
		tb.tokens += elapsed * tb.rate
		if tb.tokens > tb.capacity {
			tb.tokens = tb.capacity
		}
		tb.lastTime = now

		if tb.tokens >= 1 {
			tb.tokens--
			tb.mu.Unlock()
			return nil
		}

		// Calculate wait time for next token
		wait := time.Duration((1 - tb.tokens) / tb.rate * float64(time.Second))
		tb.mu.Unlock()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
			// retry
		}
	}
}

// RateLimiter groups token buckets by Polymarket US API endpoint category.
// Each operation must call the appropriate bucket's Wait() before the HTTP request.
type RateLimiter struct {
	Global *TokenBucket // global across all endpoints — 2000/10s
	Order  *TokenBucket // POST /v1/orders, cancel — 400/10s
	Book   *TokenBucket // GET reads (open orders, etc.) — 50/10s
}

// NewRateLimiter creates rate limiters tuned to the Polymarket US published limits.
// Capacities equal the 10-second burst allowance; rates are set to 1/10th for
// smooth continuous refill.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		Global: NewTokenBucket(200, 200), // 2000 per 10s global
		Order:  NewTokenBucket(40, 40),   // 400 per 10s order/cancel
		Book:   NewTokenBucket(5, 5),     // 50 per 10s reads
	}
}
