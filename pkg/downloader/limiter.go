package downloader

import (
	"context"
	"sync"
	"time"
)

// Limiter implements a token bucket rate limiter for controlling download speed.
// It is safe for concurrent use by multiple goroutines.
type Limiter struct {
	rate     int64   // tokens per second (bytes/sec)
	burst    int64   // max tokens that can accumulate
	tokens   float64 // current available tokens
	lastTime time.Time
	mu       sync.Mutex
}

// NewLimiter creates a rate limiter that allows up to rate bytes per second.
// The burst size defaults to rate (one second's worth of tokens).
// If rate <= 0, the limiter is a no-op (unlimited).
func NewLimiter(rate int64) *Limiter {
	if rate <= 0 {
		return &Limiter{rate: 0, burst: 0, tokens: 0}
	}
	// Allow burst of up to 1 second's worth of data, minimum 1 byte
	burst := rate
	if burst < 1 {
		burst = 1
	}
	return &Limiter{
		rate:     rate,
		burst:    burst,
		tokens:   float64(burst), // start full
		lastTime: time.Now(),
	}
}

// Allow reports whether n bytes may be consumed now.
// It does not block. If the limiter is unlimited (rate <= 0), it always returns true.
func (l *Limiter) Allow(n int) bool {
	if l.rate <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	l.refill()
	if l.tokens >= float64(n) {
		l.tokens -= float64(n)
		return true
	}
	return false
}

// Wait blocks until n bytes worth of tokens are available or ctx is cancelled.
// If the limiter is unlimited (rate <= 0), it returns immediately.
func (l *Limiter) Wait(ctx context.Context, n int) error {
	if l.rate <= 0 {
		return nil
	}

	for {
		l.mu.Lock()
		l.refill()
		if l.tokens >= float64(n) {
			l.tokens -= float64(n)
			l.mu.Unlock()
			return nil
		}
		// Calculate how long until enough tokens are available
		deficit := float64(n) - l.tokens
		waitDur := time.Duration(deficit / float64(l.rate) * float64(time.Second))
		if waitDur < time.Millisecond {
			waitDur = time.Millisecond
		}
		l.mu.Unlock()

		// Wait for either enough time or context cancellation
		timer := time.NewTimer(waitDur)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			// Try again
		}
	}
}

// refill replenishes tokens based on elapsed time. Caller must hold l.mu.
func (l *Limiter) refill() {
	now := time.Now()
	elapsed := now.Sub(l.lastTime).Seconds()
	if elapsed <= 0 {
		return
	}
	l.tokens += elapsed * float64(l.rate)
	if l.tokens > float64(l.burst) {
		l.tokens = float64(l.burst)
	}
	l.lastTime = now
}

// Rate returns the configured rate in bytes/sec. 0 means unlimited.
func (l *Limiter) Rate() int64 {
	return l.rate
}
