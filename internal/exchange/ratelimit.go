// ratelimit.go implements token-bucket rate limiting for the Polymarket CLOB API.
//
// Polymarket enforces per-category rate limits measured in requests per 10-second
// windows. This file provides a smooth token-bucket implementation that refills
// continuously (rather than in 10s bursts) to avoid hitting hard limits.
//
// Three buckets are maintained:
//   - Order:  350 burst / 50 per sec (maps to Polymarket's 3500/10s limit)
//   - Cancel: 300 burst / 30 per sec (maps to 3000/10s limit)
//   - Book:   150 burst / 15 per sec (maps to 1500/10s limit)
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

// RateLimiter groups token buckets by Polymarket API endpoint category.
// Each trading operation must call the appropriate bucket's Wait() before
// making the HTTP request.
type RateLimiter struct {
	Order  *TokenBucket // POST /orders — placing new orders
	Cancel *TokenBucket // DELETE /orders, /cancel-all, /cancel-market-orders
	Book   *TokenBucket // GET /book — order book reads
}

// NewRateLimiter creates rate limiters tuned to Polymarket's published limits.
// Capacities are set to the 10-second burst allowance, rates to 1/10th for
// smooth refill.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		Order:  NewTokenBucket(350, 50),  // 3500 per 10s window
		Cancel: NewTokenBucket(300, 30),  // 3000 per 10s window
		Book:   NewTokenBucket(150, 15),  // 1500 per 10s window
	}
}
