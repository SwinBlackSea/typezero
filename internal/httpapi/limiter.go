package httpapi

import (
	"sync"
	"time"
)

type rateEntry struct {
	windowStart time.Time
	count       int
}

type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	entries map[string]rateEntry
	now     func() time.Time
}

func newRateLimiter(limit int) *rateLimiter {
	return &rateLimiter{limit: limit, entries: make(map[string]rateEntry), now: time.Now}
}

func (l *rateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	entry := l.entries[key]
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= time.Minute {
		entry = rateEntry{windowStart: now}
	}
	if entry.count >= l.limit {
		return false
	}
	entry.count++
	l.entries[key] = entry

	if len(l.entries) > 1000 {
		for existingKey, existing := range l.entries {
			if now.Sub(existing.windowStart) >= 2*time.Minute {
				delete(l.entries, existingKey)
			}
		}
	}
	return true
}
