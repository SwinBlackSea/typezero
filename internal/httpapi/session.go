package httpapi

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// sessionState accumulates chunk transcripts that belong to a single dictation
// session. Chunks may arrive out of order; readers must not mutate internal
// maps without holding mu.
type sessionState struct {
	mu            sync.Mutex
	expectedTotal int
	finalized     bool
	createdAt     time.Time
	lastTouch     time.Time
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

func newSessionState(expectedTotal int, now time.Time) *sessionState {
	return &sessionState{
		expectedTotal: expectedTotal,
		createdAt:     now,
		lastTouch:     now,
		received:      make(map[int]string, expectedTotal),
		arrivedAt:     make(map[int]time.Time, expectedTotal),
	}
}

// store records a chunk's transcript. isLast marks the final chunk, which
// carries the authoritative chunk count: when the session was created with an
// unknown total (0, incremental uploads during recording), the final chunk's
// index+1 becomes the expected total. Returns true once the session has been
// finalized and every expected chunk has arrived.
func (s *sessionState) store(index int, text string, now time.Time, isLast bool) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if index < 0 || index >= maxChunksPerSession {
		return s.allArrived
	}
	if isLast {
		if !s.finalized {
			s.finalized = true
			if index+1 > s.expectedTotal {
				s.expectedTotal = index + 1
			}
		}
	} else if s.expectedTotal > 0 && index >= s.expectedTotal {
		// A non-final chunk beyond the declared total is out of range.
		return s.allArrived
	}
	if _, exists := s.received[index]; !exists {
		s.received[index] = text
		s.arrivedAt[index] = now
	}
	if now.After(s.lastTouch) {
		s.lastTouch = now
	}
	if !s.allArrived && s.finalized && s.expectedTotal > 0 && len(s.received) >= s.expectedTotal {
		s.allArrived = true
		s.completedAt = now
	}
	return s.allArrived
}

// growExpected raises the authoritative chunk count. Totals only ever grow:
// non-final chunks may arrive with an unknown total (0) while the recording is
// still in progress, and the final chunk carries the real count. A stale
// smaller value from a duplicated request must not shrink the session.
func (s *sessionState) growExpected(total int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if total > s.expectedTotal {
		s.expectedTotal = total
	}
}

// knownTotal returns the authoritative chunk count, or 0 while the session
// total is still unknown (no final chunk arrived yet).
func (s *sessionState) knownTotal() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.expectedTotal
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

// touch records activity at now. Called when a chunk request arrives so a
// session whose chunks are still being processed (ASR in flight, nothing
// stored yet) is not treated as idle by the janitor.
func (s *sessionState) touch(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if now.After(s.lastTouch) {
		s.lastTouch = now
	}
}

// evictionAnchor returns the timestamp the janitor compares against its
// cutoff: the later of creation time and last activity (chunk request
// arrival or chunk store). Creation time is the floor so a brand-new
// session whose first chunks are still in ASR survives the sweep.
func (s *sessionState) evictionAnchor() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	anchor := s.createdAt
	if s.lastTouch.After(anchor) {
		anchor = s.lastTouch
	}
	for _, t := range s.arrivedAt {
		if t.After(anchor) {
			anchor = t
		}
	}
	return anchor
}

// receivedCount returns the number of chunks that have arrived so far.
// Caller must not assume the returned value is stable across other callers.
func (s *sessionState) receivedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.received)
}

// SessionStore keeps sessionState objects in memory with a TTL so abandoned
// sessions do not leak. A background goroutine evicts expired sessions.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*sessionState
	ttl      time.Duration
	now      func() time.Time

	// logMu guards the logger only. It is intentionally separate from mu
	// because session lifecycle methods (getOrCreate, remove, evict,
	// waitForCompletion) call log() while holding mu; using mu for the
	// logger would deadlock.
	logMu  sync.Mutex
	logger *slog.Logger
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

// setLogger attaches a structured logger so the store can emit lifecycle
// events (create, remove, evict, wait outcome). It is safe to call before
// any goroutine touches the store.
func (s *SessionStore) setLogger(logger *slog.Logger) {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	s.logger = logger
}

func (s *SessionStore) log() *slog.Logger {
	s.logMu.Lock()
	logger := s.logger
	s.logMu.Unlock()
	if logger == nil {
		return slog.Default()
	}
	return logger
}

// getOrCreate returns the session state for id, creating one when missing.
// When the session already exists, expectedTotal is ignored.
func (s *SessionStore) getOrCreate(id string, expectedTotal int) (*sessionState, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	state, ok := s.sessions[id]
	if ok {
		if expectedTotal > state.knownTotal() {
			state.growExpected(expectedTotal)
			s.log().Debug("session.getOrCreate.grow",
				"session_id", id,
				"new_total", expectedTotal,
			)
		}
		state.touch(now)
		s.log().Debug("session.getOrCreate.found",
			"session_id", id,
			"expected_total", state.knownTotal(),
			"map_size", len(s.sessions),
		)
		return state, false, nil
	}
	if expectedTotal < 0 || expectedTotal > maxChunksPerSession {
		return nil, false, errSessionIndexOutOfRange
	}
	state = newSessionState(expectedTotal, now)
	s.sessions[id] = state
	s.log().Info("session.getOrCreate.created",
		"session_id", id,
		"expected_total", expectedTotal,
		"map_size", len(s.sessions),
	)
	return state, true, nil
}

// maxChunksPerSession bounds the number of chunks a single session can hold.
// With the current 30s step and 5min max recording, 10 chunks is the
// realistic upper bound; legacy 8s-step clients produce at most 38. We cap
// at 64 to leave headroom. Anything larger is almost certainly a malicious
// or buggy client trying to exhaust memory.
const maxChunksPerSession = 64

// remove deletes a session and returns whether it existed.
func (s *SessionStore) remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[id]
	delete(s.sessions, id)
	s.log().Info("session.remove",
		"session_id", id,
		"existed", ok,
		"map_size", len(s.sessions),
	)
	return ok
}

// evict removes sessions whose eviction anchor (later of creation time and
// last activity) is older than ttl. Returns the number of sessions removed.
// Expired candidates are collected under the store lock and then checked
// individually so we do not hold the store lock while waiting for each
// session's own mutex.
func (s *SessionStore) evict() (removed int) {
	cutoff := s.now().Add(-s.ttl)
	s.mu.Lock()
	candidates := make([]string, 0, len(s.sessions))
	for id, state := range s.sessions {
		if state.evictionAnchor().Before(cutoff) {
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
		anchor := state.evictionAnchor()
		if anchor.Before(cutoff) {
			delete(s.sessions, id)
			removed++
			s.log().Info("session.evict",
				"session_id", id,
				"anchor", anchor,
				"cutoff", cutoff,
				"map_size", len(s.sessions),
			)
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
		s.log().Warn("session.waitForCompletion.missing",
			"session_id", id,
		)
		return nil, errSessionMissing
	}
	s.log().Info("session.waitForCompletion.start",
		"session_id", id,
		"expected_total", state.expectedTotal,
		"received", state.receivedCount(),
	)
	for {
		if state.isComplete() {
			s.log().Info("session.waitForCompletion.complete",
				"session_id", id,
				"received", state.receivedCount(),
			)
			return state.snapshot(), nil
		}
		select {
		case <-ctx.Done():
			s.log().Warn("session.waitForCompletion.ctxDone",
				"session_id", id,
				"err", ctx.Err(),
			)
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

// chunkSnapshot returns the received count, expected total, and complete
// flag for a session, or zero values when the session is absent. Used by
// observability; safe to call concurrently.
func (s *SessionStore) chunkSnapshot(id string) (received, expectedTotal int, complete bool) {
	s.mu.Lock()
	state, ok := s.sessions[id]
	s.mu.Unlock()
	if !ok {
		return 0, 0, false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return len(state.received), state.expectedTotal, state.allArrived
}
