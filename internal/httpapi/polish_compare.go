package httpapi

// polish_compare.go implements an opt-in polish comparison mode
// (POLISH_COMPARE=1). For every dictation the server polishes the same raw
// text with three prompt variants — the classic prompt plus the term table
// (pre-redesign behavior), the v2 capability-driven prompt plus the term
// table, and the v2 prompt without the table — and records each result in a
// JSONL file (default /tmp/polish_compare.jsonl) together with the raw text
// and the live (primary) polished text, so the contribution of the term table
// and of the prompt redesign can be judged side by side on real recordings.
//
// Like ASR compare mode, the output contains transcripts by design but is
// written only when explicitly enabled, never to server.log, and never to the
// repo. Audio and API keys are not recorded.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"typezero/internal/provider"
)

type polishCompareRecorder struct {
	mu     sync.Mutex
	path   string
	file   *os.File
	logger *slog.Logger
	text   provider.VariantPolish
}

func newPolishCompareRecorder(path string, text provider.Text, logger *slog.Logger) *polishCompareRecorder {
	variant, ok := text.(provider.VariantPolish)
	if !ok {
		if logger != nil {
			logger.Warn("POLISH_COMPARE enabled but text provider has no variant support; comparison disabled")
		}
		return nil
	}
	return &polishCompareRecorder{path: path, logger: logger, text: variant}
}

// run polishes the same chunks with every variant in the background. The
// request context may die as soon as the client gets its response, so each
// variant call uses a fresh context with its own bounded timeout. Best-effort:
// failures are recorded per variant, never surfaced to the client.
func (r *polishCompareRecorder) run(sessionID string, chunkTotal int, chunks []string, primaryPolished string) {
	if r == nil || len(chunks) == 0 {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		arms := make(map[string]string)
		errs := make(map[string]string)
		for name, variant := range map[string]provider.PolishVariant{
			"classic_with_table": provider.PolishClassicWithTable,
			"v2_with_table":      provider.PolishV2WithTable,
			"v2_clean":           provider.PolishV2Clean,
		} {
			text, err := r.text.PolishVariant(ctx, chunks, variant)
			if err != nil {
				errs[name] = err.Error()
				continue
			}
			arms[name] = text
		}
		record := map[string]any{
			"time":             time.Now().Format(time.RFC3339Nano),
			"kind":             "polish_compare",
			"session_id":       sessionID,
			"chunk_count":      chunkTotal,
			"raw_text":         strings.Join(chunks, "\n"),
			"primary_polished": primaryPolished,
			"polish":           arms,
			"errors":           errs,
		}
		r.write(record)
	}()
}

func (r *polishCompareRecorder) write(record map[string]any) {
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
				r.logger.Warn("polish compare file open failed", "path", r.path, "error", err)
			}
			return
		}
	}
	if _, err := fmt.Fprintf(r.file, "%s\n", line); err != nil && r.logger != nil {
		r.logger.Warn("polish compare write failed", "error", err)
	}
}
