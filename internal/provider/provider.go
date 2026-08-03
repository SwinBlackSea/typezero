package provider

import (
	"context"
	"errors"
)

type Audio struct {
	Data      []byte
	MediaType string
	Filename  string
}

// ErrEmptyTranscript is returned by a Speech implementation when the audio
// processed but yielded no text (silence, very short input, etc.). Callers
// that operate on multiple transcript chunks — e.g. the chunked dictation
// pipeline — should treat this as a soft signal: store the empty text and
// continue, so a silent tail in one chunk does not poison the whole session.
var ErrEmptyTranscript = errors.New("provider returned empty transcript")

type Speech interface {
	Transcribe(ctx context.Context, audio Audio) (string, error)
}

type Text interface {
	Polish(ctx context.Context, rawText string) (string, error)
	// PolishChunks merges and polishes multiple transcript chunks that share
	// overlap. Each entry of chunks is the raw transcript for one chunk,
	// ordered by chunk index. The implementation is expected to drop the
	// duplicated overlap region between neighbours and produce a single
	// polished string. PolishChunks is only invoked when there is more than
	// one chunk; single-chunk recordings go through Polish.
	PolishChunks(ctx context.Context, chunks []string) (string, error)
}
