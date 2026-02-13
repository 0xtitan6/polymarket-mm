package exchange

import (
	"context"
	"testing"
	"time"
)

func TestNewTokenBucketStartsFull(t *testing.T) {
	t.Parallel()
	tb := NewTokenBucket(10, 1)
	if tb.tokens != 10 {
		t.Errorf("tokens = %v, want 10", tb.tokens)
	}
}

func TestTokenBucketWaitImmediate(t *testing.T) {
	t.Parallel()
	tb := NewTokenBucket(5, 1)

	// Should consume tokens without blocking
	for i := 0; i < 5; i++ {
		start := time.Now()
		if err := tb.Wait(context.Background()); err != nil {
			t.Fatalf("Wait() returned error: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
			t.Errorf("Wait() took %v, expected immediate (token %d)", elapsed, i)
		}
	}
}

func TestTokenBucketWaitBlocks(t *testing.T) {
	t.Parallel()
	// 1 token capacity, refills at 10/sec → ~100ms per token
	tb := NewTokenBucket(1, 10)

	// Consume the single token
	if err := tb.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Next Wait should block ~100ms
	start := time.Now()
	if err := tb.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if elapsed < 50*time.Millisecond {
		t.Errorf("expected blocking ~100ms, got %v", elapsed)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("blocked too long: %v", elapsed)
	}
}

func TestTokenBucketContextCancelled(t *testing.T) {
	t.Parallel()
	tb := NewTokenBucket(1, 0.1) // very slow refill

	// Exhaust the token
	_ = tb.Wait(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := tb.Wait(ctx)
	if err == nil {
		t.Error("expected context error, got nil")
	}
}
