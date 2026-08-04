package httpapi

import (
	"sync"
	"time"
)

// rateLimiter implements a per-key token bucket. Tokens refill at a rate of
// limit per minute and the bucket holds at most limit tokens, so a short
// burst (all chunks of one dictation session uploaded in parallel) passes
// while the sustained rate stays at limit requests per minute. A fixed
// window could not express this: a session whose chunks straddled the
// window boundary was rejected even though the average rate was fine.
type rateLimiter struct {
	mu      sync.Mutex
	rate    float64 // tokens per nanosecond
	burst   float64 // bucket capacity
	entries map[string]*bucket
	now     func() time.Time
}

type bucket struct {
	tokens   float64
	refilled time.Time
}

func newRateLimiter(limit int) *rateLimiter {
	if limit < 1 {
		limit = 1
	}
	return &rateLimiter{
		rate:    float64(limit) / float64(time.Minute),
		burst:   float64(limit),
		entries: make(map[string]*bucket),
		now:     time.Now,
	}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	entry, ok := l.entries[key]
	if !ok {
		entry = &bucket{tokens: l.burst, refilled: now}
		l.entries[key] = entry
	} else if elapsed := now.Sub(entry.refilled); elapsed > 0 {
		entry.tokens = min(l.burst, entry.tokens+float64(elapsed)*l.rate)
		entry.refilled = now
	}
	if entry.tokens < 1 {
		return false
	}
	entry.tokens--

	if len(l.entries) > 1000 {
		for existingKey, existing := range l.entries {
			if now.Sub(existing.refilled) >= 2*time.Minute {
				delete(l.entries, existingKey)
			}
		}
	}
	return true
}
