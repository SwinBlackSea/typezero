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
	state.store(0, "文本", arrival, false)

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

// Incremental sessions are created with an unknown chunk total (0) while the
// client is still recording. The final chunk must finalize the session and
// establish the authoritative count even when it arrives before some of the
// non-final chunks.
func TestIncrementalSessionFinalizeSetsTotal(t *testing.T) {
	now := time.Now()
	store := newSessionStore(time.Minute)
	store.now = func() time.Time { return now }

	state, _, err := store.getOrCreate("incr", 0)
	if err != nil {
		t.Fatal(err)
	}
	if total := state.knownTotal(); total != 0 {
		t.Fatalf("expected unknown total 0, got %d", total)
	}

	// Background chunks arrive with unknown total and must not complete the
	// session by themselves.
	if complete := state.store(0, "甲", now, false); complete {
		t.Fatal("non-final chunk completed an unfinalized session")
	}
	if complete := state.store(1, "乙", now, false); complete {
		t.Fatal("non-final chunk completed an unfinalized session")
	}

	// The final chunk carries the authoritative count and finalizes.
	complete := state.store(2, "丙", now, true)
	if !complete {
		t.Fatal("finalized session with all chunks stored must be complete")
	}
	if total := state.knownTotal(); total != 3 {
		t.Fatalf("expected finalized total 3, got %d", total)
	}
}

// The final chunk may arrive before the last non-final chunks (parallel
// uploads with different ASR latencies). The session stays incomplete until
// every index below the authoritative total is stored.
func TestIncrementalSessionWaitsForMissingChunks(t *testing.T) {
	now := time.Now()
	store := newSessionStore(time.Minute)
	store.now = func() time.Time { return now }

	state, _, err := store.getOrCreate("incr-2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if complete := state.store(0, "甲", now, false); complete {
		t.Fatal("non-final chunk completed an unfinalized session")
	}
	if complete := state.store(2, "丙", now, true); complete {
		t.Fatal("finalized session must wait for chunk 1")
	}
	if complete := state.store(1, "乙", now, false); !complete {
		t.Fatal("session with every chunk stored must be complete")
	}
}

// A later non-final chunk may carry a larger guessed total; the session must
// grow instead of rejecting it, but a smaller stale total must not shrink.
func TestIncrementalSessionGrowOnlyTotals(t *testing.T) {
	now := time.Now()
	store := newSessionStore(time.Minute)
	store.now = func() time.Time { return now }

	state, _, err := store.getOrCreate("incr-3", 5)
	if err != nil {
		t.Fatal(err)
	}
	state.growExpected(7)
	if total := state.knownTotal(); total != 7 {
		t.Fatalf("expected grown total 7, got %d", total)
	}
	state.growExpected(3)
	if total := state.knownTotal(); total != 7 {
		t.Fatalf("stale smaller total shrank session to %d", total)
	}
}
