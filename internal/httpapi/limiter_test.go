package httpapi

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsBurstUpToCapacity(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(5)
	limiter.now = func() time.Time { return now }

	for i := 0; i < 5; i++ {
		if !limiter.allow("client") {
			t.Fatalf("request %d within burst rejected", i+1)
		}
	}
	if limiter.allow("client") {
		t.Fatal("request beyond burst capacity allowed")
	}
}

// A whole chunked session (up to 38 legacy chunks, ~10 with the 30s step)
// must pass in one burst under the default 60/min limit.
func TestRateLimiterAcceptsFullChunkedSessionInOneBurst(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(60)
	limiter.now = func() time.Time { return now }

	for i := 0; i < 38; i++ {
		if !limiter.allow("client") {
			t.Fatalf("chunk %d of 38 rejected", i+1)
		}
	}
}

func TestRateLimiterRefillsOverTime(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(60)
	limiter.now = func() time.Time { return now }

	for i := 0; i < 60; i++ {
		limiter.allow("client")
	}
	if limiter.allow("client") {
		t.Fatal("request beyond capacity allowed")
	}

	// 60/min = 1 token per second.
	now = now.Add(time.Second)
	if !limiter.allow("client") {
		t.Fatal("token not refilled after one second")
	}
	if limiter.allow("client") {
		t.Fatal("more than one token refilled after one second")
	}

	// A full minute refills back to the burst cap, not beyond.
	now = now.Add(time.Minute)
	for i := 0; i < 60; i++ {
		if !limiter.allow("client") {
			t.Fatalf("request %d after full refill rejected", i+1)
		}
	}
	if limiter.allow("client") {
		t.Fatal("bucket exceeded capacity after refill")
	}
}

func TestRateLimiterTracksKeysIndependently(t *testing.T) {
	now := time.Now()
	limiter := newRateLimiter(1)
	limiter.now = func() time.Time { return now }

	if !limiter.allow("a") {
		t.Fatal("first request for key a rejected")
	}
	if limiter.allow("a") {
		t.Fatal("second request for key a allowed")
	}
	if !limiter.allow("b") {
		t.Fatal("first request for key b rejected")
	}
}
