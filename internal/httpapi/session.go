package httpapi

import (
	"context"
	"sync"
	"time"
)

// sessionState accumulates chunk transcripts that belong to a single dictation
// session. Chunks may arrive out of order; readers must not mutate internal
// maps without holding mu.
type sessionState struct {
	mu            sync.Mutex
	expectedTotal int
	received      map[int]string
	arrivedAt     map[int]time.Time
	completedAt   time.Time
	allArrived    bool

	// Cumulative ASR timing across all chunks in the session. Updated on
	// every successful Transcribe so the final response can emit a single
	// authoritative "asr_chunks" duration in Server-Timing.
	asrTotal time.Duration
	asrMax   time.Duration
}

func newSessionState(expectedTotal int) *sessionState {
	return &sessionState{
		expectedTotal: expectedTotal,
		received:      make(map[int]string, expectedTotal),
		arrivedAt:     make(map[int]time.Time, expectedTotal),
	}
}

// store records a chunk's transcript. Returns true once every expected chunk
// has arrived.
func (s *sessionState) store(index int, text string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= s.expectedTotal {
		return s.allArrived
	}
	if _, exists := s.received[index]; !exists {
		s.received[index] = text
		s.arrivedAt[index] = now
	}
	if !s.allArrived && len(s.received) >= s.expectedTotal {
		s.allArrived = true
		s.completedAt = now
	}
	return s.allArrived
}

// snapshot returns transcripts ordered by chunk index. Caller must not mutate
// the returned slice.
func (s *sessionState) snapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, s.expectedTotal)
	for i := range out {
		out[i] = s.received[i]
	}
	return out
}

// isComplete reports whether every expected chunk has been stored.
func (s *sessionState) isComplete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.allArrived
}

// recordASR folds one chunk's ASR elapsed time into the running totals.
// Safe to call concurrently with other sessionState methods.
func (s *sessionState) recordASR(elapsed time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asrTotal += elapsed
	if elapsed > s.asrMax {
		s.asrMax = elapsed
	}
}

// asrMetrics returns the cumulative ASR timings observed so far.
func (s *sessionState) asrMetrics() (total, max time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.asrTotal, s.asrMax
}

// lastSeen returns the most recent arrival timestamp, or zero if no chunks
// have arrived.
func (s *sessionState) lastSeen() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	var last time.Time
	for _, t := range s.arrivedAt {
		if t.After(last) {
			last = t
		}
	}
	return last
}

// SessionStore keeps sessionState objects in memory with a TTL so abandoned
// sessions do not leak. A background goroutine evicts expired sessions.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionState
	ttl      time.Duration
	now      func() time.Time
}

func newSessionStore(ttl time.Duration) *SessionStore {
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}
	return &SessionStore{
		sessions: make(map[string]*sessionState),
		ttl:      ttl,
		now:      time.Now,
	}
}

// getOrCreate returns the session state for id, creating one when missing.
// When the session already exists, expectedTotal is ignored.
func (s *SessionStore) getOrCreate(id string, expectedTotal int) (*sessionState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, ok := s.sessions[id]
	if ok {
		if state.expectedTotal != expectedTotal {
			return nil, false, errSessionTotalMismatch
		}
		return state, false, nil
	}
	if expectedTotal <= 0 {
		return nil, false, errSessionIndexOutOfRange
	}
	if expectedTotal > maxChunksPerSession {
		return nil, false, errSessionTotalMismatch
	}
	state = newSessionState(expectedTotal)
	s.sessions[id] = state
	return state, true, nil
}

// maxChunksPerSession bounds the number of chunks a single session can hold.
// With 8s step and 5min max recording, 38 chunks is the realistic upper
// bound; we round up to 64 to leave headroom. Anything larger is almost
// certainly a malicious or buggy client trying to exhaust memory.
const maxChunksPerSession = 64

// remove deletes a session and returns whether it existed.
func (s *SessionStore) remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[id]
	delete(s.sessions, id)
	return ok
}

// evict removes sessions whose last activity is older than ttl. Returns the
// number of sessions removed. Expired candidates are collected under the
// store lock and then checked individually so we do not hold the store lock
// while waiting for each session's own mutex.
func (s *SessionStore) evict() (removed int) {
	cutoff := s.now().Add(-s.ttl)
	s.mu.Lock()
	candidates := make([]string, 0, len(s.sessions))
	for id, state := range s.sessions {
		last := state.lastSeen()
		if last.IsZero() || last.Before(cutoff) {
			candidates = append(candidates, id)
		}
	}
	s.mu.Unlock()
	for _, id := range candidates {
		s.mu.Lock()
		// Re-check: the session may have been reactivated or removed while
		// we were checking other candidates.
		state, ok := s.sessions[id]
		if !ok {
			s.mu.Unlock()
			continue
		}
		last := state.lastSeen()
		if last.IsZero() || last.Before(cutoff) {
			delete(s.sessions, id)
			removed++
		}
		s.mu.Unlock()
	}
	return removed
}

// runJanitor evicts expired sessions every interval until ctx is cancelled.
func (s *SessionStore) runJanitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = s.ttl / 2
		if interval <= 0 {
			interval = time.Minute
		}
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.evict()
		}
	}
}

// waitForCompletion blocks until every expected chunk has arrived or ctx is
// done. It returns the ordered transcripts on success.
func (s *SessionStore) waitForCompletion(ctx context.Context, id string, poll time.Duration) ([]string, error) {
	if poll <= 0 {
		poll = 10 * time.Millisecond
	}
	s.mu.Lock()
	state := s.sessions[id]
	s.mu.Unlock()
	if state == nil {
		return nil, errSessionMissing
	}
	for {
		if state.isComplete() {
			return state.snapshot(), nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// chunkMeta reports observed metadata for a session. Used by observability
// hooks; safe to call concurrently.
type chunkMeta struct {
	Count     int
	Complete  bool
	FirstSeen time.Time
	LastSeen  time.Time
}

func (s *SessionStore) chunkMeta(id string) (chunkMeta, bool) {
	s.mu.Lock()
	state, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok {
		return chunkMeta{}, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	var first, last time.Time
	for _, t := range state.arrivedAt {
		if first.IsZero() || t.Before(first) {
			first = t
		}
		if t.After(last) {
			last = t
		}
	}
	return chunkMeta{
		Count:     len(state.received),
		Complete:  state.allArrived,
		FirstSeen: first,
		LastSeen:  last,
	}, true
}