package httpapi

import (
	"testing"
	"time"
)

// Regression: the janitor used to treat sessions without any stored chunk
// (lastSeen zero) as immediately evictable. Right after a session is
// created, all of its chunks can still be inside slow ASR calls with
// nothing stored yet; evicting at that point killed the session mid-flight
// and turned the final chunk's wait into "session missing".
func TestEvictKeepsSessionWithASRInFlight(t *testing.T) {
	now := time.Now()
	store := newSessionStore(time.Minute)
	store.now = func() time.Time { return now }

	if _, _, err := store.getOrCreate("s1", 4); err != nil {
		t.Fatal(err)
	}

	// Half the TTL passes; no chunk has been stored yet because ASR is slow.
	now = now.Add(30 * time.Second)
	if removed := store.evict(); removed != 0 {
		t.Fatalf("evict removed %d sessions, want 0", removed)
	}
	if _, ok := store.chunkMeta("s1"); !ok {
		t.Fatal("session evicted while its chunks were still in ASR")
	}
}

func TestEvictRemovesAbandonedSession(t *testing.T) {
	now := time.Now()
	store := newSessionStore(time.Minute)
	store.now = func() time.Time { return now }

	if _, _, err := store.getOrCreate("s1", 4); err != nil {
		t.Fatal(err)
	}

	now = now.Add(time.Minute + time.Second)
	if removed := store.evict(); removed != 1 {
		t.Fatalf("evict removed %d sessions, want 1", removed)
	}
	if _, ok := store.chunkMeta("s1"); ok {
		t.Fatal("abandoned session survived past TTL")
	}
}

// A chunk stored late must extend the session's lifetime past the
// creation-time TTL.
func TestEvictUsesLatestChunkArrival(t *testing.T) {
	now := time.Now()
	store := newSessionStore(time.Minute)
	store.now = func() time.Time { return now }

	state, _, err := store.getOrCreate("s1", 4)
	if err != nil {
		t.Fatal(err)
	}

	arrival := now.Add(50 * time.Second)
	state.store(0, "文本", arrival)

	// Past creation+TTL but within arrival+TTL.
	now = now.Add(61 * time.Second)
	if removed := store.evict(); removed != 0 {
		t.Fatalf("evict removed %d sessions, want 0", removed)
	}

	now = arrival.Add(time.Minute + time.Second)
	if removed := store.evict(); removed != 1 {
		t.Fatalf("evict removed %d sessions, want 1", removed)
	}
}

// A chunk request arriving for an existing session (before its ASR even
// starts) must refresh the session so its in-flight ASR survives the TTL.
func TestEvictRefreshesOnChunkRequest(t *testing.T) {
	now := time.Now()
	store := newSessionStore(time.Minute)
	store.now = func() time.Time { return now }

	if _, _, err := store.getOrCreate("s1", 4); err != nil {
		t.Fatal(err)
	}

	// A later chunk request arrives just before the TTL would expire.
	now = now.Add(50 * time.Second)
	if _, _, err := store.getOrCreate("s1", 4); err != nil {
		t.Fatal(err)
	}

	// 80s since creation, 30s since the last request: must survive.
	now = now.Add(30 * time.Second)
	if removed := store.evict(); removed != 0 {
		t.Fatalf("evict removed %d sessions, want 0", removed)
	}

	// 70s since the last request: must be evicted.
	now = now.Add(40 * time.Second)
	if removed := store.evict(); removed != 1 {
		t.Fatalf("evict removed %d sessions, want 1", removed)
	}
}
