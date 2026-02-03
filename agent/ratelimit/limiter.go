package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xichan96/cortex/pkg/errors"
)

type RateLimiter interface {
	Allow(ctx context.Context) error
	Wait(ctx context.Context) error
	Stats() Stats
}

type Stats struct {
	Allowed  int64
	Rejected int64
	WaitTime time.Duration
	HitRate  float64
}

type TokenBucketLimiter struct {
	tokens     atomic.Int64
	capacity   int64
	refillRate int64
	lastRefill atomic.Int64
	mu         sync.Mutex

	allowed   atomic.Int64
	rejected  atomic.Int64
	totalWait atomic.Int64
}

func NewTokenBucketLimiter(capacity int64, refillRate int64) *TokenBucketLimiter {
	limiter := &TokenBucketLimiter{
		capacity:   capacity,
		refillRate: refillRate,
	}
	limiter.tokens.Store(capacity)
	limiter.lastRefill.Store(time.Now().UnixNano())
	return limiter
}

func (t *TokenBucketLimiter) refill() {
	now := time.Now().UnixNano()
	lastRefill := t.lastRefill.Load()
	elapsed := time.Duration(now - lastRefill)

	if elapsed <= 0 {
		return
	}

	tokensToAdd := int64(elapsed.Seconds() * float64(t.refillRate))
	if tokensToAdd > 0 {
		t.mu.Lock()
		current := t.tokens.Load()
		newTokens := current + tokensToAdd
		if newTokens > t.capacity {
			newTokens = t.capacity
		}
		t.tokens.Store(newTokens)
		t.lastRefill.Store(now)
		t.mu.Unlock()
	}
}

func (t *TokenBucketLimiter) Allow(ctx context.Context) error {
	t.refill()

	for {
		tokens := t.tokens.Load()
		if tokens <= 0 {
			t.rejected.Add(1)
			return errors.ErrRateLimitExceeded
		}

		if t.tokens.CompareAndSwap(tokens, tokens-1) {
			t.allowed.Add(1)
			return nil
		}
	}
}

func (t *TokenBucketLimiter) Wait(ctx context.Context) error {
	startTime := time.Now()
	defer func() {
		waitDuration := time.Since(startTime)
		t.totalWait.Add(int64(waitDuration))
	}()

	// Calculate adaptive polling interval based on refill rate
	// We want to poll slightly faster than the refill rate to capture tokens as soon as they are available
	// but not too fast to burn CPU.
	// Interval = 1s / refillRate / 2 (Nyquist-ish, check twice per period)
	var pollInterval time.Duration
	if t.refillRate > 0 {
		pollInterval = time.Second / time.Duration(t.refillRate)
		if pollInterval > 100*time.Millisecond {
			pollInterval = 100 * time.Millisecond // Cap max wait at 100ms for responsiveness
		} else if pollInterval < 1*time.Millisecond {
			pollInterval = 1 * time.Millisecond // Cap min wait at 1ms
		}
	} else {
		pollInterval = 10 * time.Millisecond // Fallback
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	for {
		// Check context first
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if err := t.Allow(ctx); err == nil {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Continue loop to try Allow() again
		}
	}
}

func (t *TokenBucketLimiter) Stats() Stats {
	allowed := t.allowed.Load()
	rejected := t.rejected.Load()
	total := allowed + rejected

	var hitRate float64
	if total > 0 {
		hitRate = float64(allowed) / float64(total)
	}

	waitTime := time.Duration(t.totalWait.Load())
	if allowed > 0 {
		waitTime = waitTime / time.Duration(allowed)
	}

	return Stats{
		Allowed:  allowed,
		Rejected: rejected,
		WaitTime: waitTime,
		HitRate:  hitRate,
	}
}
