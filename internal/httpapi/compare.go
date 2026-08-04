package httpapi

// compare.go implements an opt-in ASR comparison mode (ASR_COMPARE=1). For
// every dictation chunk the server transcribes the audio with the primary
// provider as usual, then asynchronously runs the same audio through a second
// provider and records speed + raw transcript side by side in a JSONL file
// (default /tmp/asr_compare.jsonl). At session completion it also runs the
// shared polish model over the second provider's chunks and records the final
// polished text so accuracy can be judged end to end.
//
// The comparison output contains transcripts by design (that is the point of
// the mode) but is written only when explicitly enabled, never to server.log,
// and never to the repo. Audio and API keys are not recorded.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"sync"
	"time"

	"typezero/internal/provider"
)

type storedCompareChunk struct {
	Index   int
	AudioMS int64
	Text    string
	ASRMS   int64
}

type finalizeRequest struct {
	chunkTotal      int
	primaryPolished string
}

type compareRecorder struct {
	mu            sync.Mutex
	path          string
	file          *os.File
	logger        *slog.Logger
	timeout       time.Duration
	primaryLabel  string
	compareLabel  string
	speech        provider.Speech
	text          provider.Text
	sessionChunks map[string][]storedCompareChunk
	started       map[string]time.Time
	finalizeReq   map[string]finalizeRequest
}

func newCompareRecorder(path string, timeout time.Duration, primaryLabel, compareLabel string, speech provider.Speech, text provider.Text, logger *slog.Logger) *compareRecorder {
	r := &compareRecorder{
		path:          path,
		logger:        logger,
		timeout:       timeout,
		primaryLabel:  primaryLabel,
		compareLabel:  compareLabel,
		speech:        speech,
		text:          text,
		sessionChunks: make(map[string][]storedCompareChunk),
		started:       make(map[string]time.Time),
		finalizeReq:   make(map[string]finalizeRequest),
	}
	go r.runJanitor()
	return r
}

func (r *compareRecorder) runJanitor() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		r.mu.Lock()
		for sessionID, started := range r.started {
			if time.Since(started) > 15*time.Minute {
				delete(r.sessionChunks, sessionID)
				delete(r.started, sessionID)
				delete(r.finalizeReq, sessionID)
			}
		}
		r.mu.Unlock()
	}
}

// compareChunk runs the second provider over the same audio in the background.
// The request context may die as soon as the client gets its response, so the
// comparison uses a fresh context with the provider timeout.
func (a *API) compareChunk(sessionID string, chunkIndex int, audioMS int64, audio provider.Audio, primaryText string, primaryASR time.Duration) {
	if a.compare == nil || a.compare.speech == nil {
		return
	}
	a.compare.startedCompare(sessionID)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), a.compare.timeout)
		defer cancel()
		asrStarted := time.Now()
		text, err := a.compare.speech.Transcribe(ctx, audio)
		asrMS := time.Since(asrStarted).Milliseconds()
		record := map[string]any{
			"time":             time.Now().Format(time.RFC3339Nano),
			"kind":             "chunk",
			"session_id":       sessionID,
			"chunk_index":      chunkIndex,
			"audio_ms":         audioMS,
			"primary_provider": a.compare.primaryLabel,
			"primary_asr_ms":   primaryASR.Milliseconds(),
			"primary_text":     primaryText,
			"compare_provider": a.compare.compareLabel,
			"compare_asr_ms":   asrMS,
			"compare_text":     text,
		}
		if err != nil {
			record["compare_error"] = err.Error()
		}
		a.compare.storeCompareResult(sessionID, chunkIndex, audioMS, text, asrMS)
		a.compare.write(record)
	}()
}

// compareFinalize polishes the second provider's chunks and appends the
// end-to-end comparison. The comparison provider runs asynchronously and can
// take tens of seconds per chunk, so the polish is deferred until every chunk
// has a result. Best-effort: failures just produce an entry with
// compare_error, never a client-visible error.
func (a *API) compareFinalize(sessionID string, chunkTotal int, primaryPolished string) {
	if a.compare == nil {
		return
	}
	a.compare.markFinalize(sessionID, chunkTotal, primaryPolished)
}

func (r *compareRecorder) markFinalize(sessionID string, chunkTotal int, primaryPolished string) {
	if chunkTotal < 1 {
		chunkTotal = 1
	}
	r.mu.Lock()
	r.finalizeReq[sessionID] = finalizeRequest{chunkTotal: chunkTotal, primaryPolished: primaryPolished}
	_, hasChunks := r.sessionChunks[sessionID]
	r.mu.Unlock()
	if hasChunks {
		r.maybeFinalize(sessionID)
	}
}

func (r *compareRecorder) maybeFinalize(sessionID string) {
	r.mu.Lock()
	req, hasReq := r.finalizeReq[sessionID]
	chunks, hasChunks := r.sessionChunks[sessionID]
	if !hasReq || !hasChunks || len(chunks) < req.chunkTotal {
		r.mu.Unlock()
		return
	}
	delete(r.sessionChunks, sessionID)
	delete(r.started, sessionID)
	delete(r.finalizeReq, sessionID)
	sort.Slice(chunks, func(i, j int) bool { return chunks[i].Index < chunks[j].Index })
	r.mu.Unlock()

	go func() {
		var raw []string
		var compareASRTotal time.Duration
		for _, c := range chunks {
			raw = append(raw, c.Text)
			compareASRTotal += time.Duration(c.ASRMS) * time.Millisecond
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		polishStarted := time.Now()
		polished, err := r.text.PolishChunks(ctx, raw)
		polishMS := time.Since(polishStarted).Milliseconds()
		record := map[string]any{
			"time":                 time.Now().Format(time.RFC3339Nano),
			"kind":                 "final",
			"session_id":           sessionID,
			"chunk_count":          len(chunks),
			"primary_provider":     r.primaryLabel,
			"compare_provider":     r.compareLabel,
			"compare_asr_total_ms": compareASRTotal.Milliseconds(),
			"compare_polish_ms":    polishMS,
			"primary_polished":     req.primaryPolished,
			"compare_polished":     polished,
		}
		if err != nil {
			record["compare_polish_error"] = err.Error()
		}
		r.write(record)
	}()
}

func (r *compareRecorder) startedCompare(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.started[sessionID]; !ok {
		r.started[sessionID] = time.Now()
	}
}

func (r *compareRecorder) storeCompareResult(sessionID string, chunkIndex int, audioMS int64, text string, asrMS int64) {
	r.mu.Lock()
	chunks := r.sessionChunks[sessionID]
	chunks = append(chunks, storedCompareChunk{Index: chunkIndex, AudioMS: audioMS, Text: text, ASRMS: asrMS})
	r.sessionChunks[sessionID] = chunks
	complete := false
	if req, ok := r.finalizeReq[sessionID]; ok {
		complete = len(chunks) >= req.chunkTotal
	}
	r.mu.Unlock()
	if complete {
		r.maybeFinalize(sessionID)
	}
}

func (r *compareRecorder) write(record map[string]any) {
	line, err := json.Marshal(record)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.file == nil {
		r.file, err = os.OpenFile(r.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			if r.logger != nil {
				r.logger.Warn("asr compare file open failed", "path", r.path, "error", err)
			}
			return
		}
	}
	if _, err := fmt.Fprintf(r.file, "%s\n", line); err != nil && r.logger != nil {
		r.logger.Warn("asr compare write failed", "error", err)
	}
}
