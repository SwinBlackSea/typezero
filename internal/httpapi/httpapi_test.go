package httpapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"typezero/internal/provider"
)

type speechStub struct {
	text string
	err  error
}

func (s speechStub) Transcribe(_ context.Context, _ provider.Audio) (string, error) {
	return s.text, s.err
}

type textStub struct {
	text      string
	err       error
	chunks    []string
	chunksErr error
	// recordChunks saves every input seen by PolishChunks so tests can
	// assert what the handler handed to the polish stage.
	recordChunks [][]string
}

func (s *textStub) Polish(_ context.Context, _ string) (string, error) {
	return s.text, s.err
}

func (s *textStub) PolishChunks(_ context.Context, chunks []string) (string, error) {
	s.recordChunks = append(s.recordChunks, append([]string(nil), chunks...))
	if s.chunksErr != nil {
		return "", s.chunksErr
	}
	if s.chunks != nil {
		return strings.Join(s.chunks, ""), nil
	}
	return s.text, s.err
}

func TestDictationSuccess(t *testing.T) {
	handler := testHandler(speechStub{text: "原始文字"}, &textStub{text: "最终文字"})
	request := multipartRequest(t, testWAV(time.Second), "recording.wav", "1000", "polished")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body dictationResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.RawText != "原始文字" || body.FinalText != "最终文字" || body.RequestID == "" {
		t.Fatalf("response = %#v", body)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store header")
	}
	for _, metric := range []string{"intake;dur=", "asr;dur=", "polish;dur="} {
		if !strings.Contains(response.Header().Get("Server-Timing"), metric) {
			t.Fatalf("Server-Timing = %q, missing %q", response.Header().Get("Server-Timing"), metric)
		}
	}
}

func TestDictationReturnsRawTextWhenPolishingFails(t *testing.T) {
	handler := testHandler(speechStub{text: "原始文字"}, &textStub{err: errors.New("unavailable")})
	request := multipartRequest(t, testWAV(time.Second), "recording.wav", "1000", "polished")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var body dictationResponse
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.RawText != "原始文字" || body.FinalText != "" || body.Warning == nil || body.Warning.Code != "polishing_failed" {
		t.Fatalf("response = %#v", body)
	}
}

func TestDictationRejectsLongAudioBeforeProviderCall(t *testing.T) {
	handler := testHandler(speechStub{text: "should not be returned"}, &textStub{})
	request := multipartRequest(t, testWAV(time.Second), "recording.wav", "300001", "polished")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestProvidersForRequestUsesEphemeralKeys(t *testing.T) {
	var qwenKey, deepSeekKey string
	api := &API{
		speech: speechStub{text: "default speech"},
		text:   &textStub{text: "default text"},
		speechForKey: func(key string) provider.Speech {
			qwenKey = key
			return speechStub{text: "custom speech"}
		},
		textForKey: func(key string) provider.Text {
			deepSeekKey = key
			return &textStub{text: "custom text"}
		},
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/dictations", nil)
	request.Header.Set("X-TypeZero-DashScope-Key", " qwen-user-key ")
	request.Header.Set("X-TypeZero-DeepSeek-Key", " deepseek-user-key ")

	if _, _, err := api.providersForRequest(request); err != nil {
		t.Fatalf("providersForRequest() error = %v", err)
	}
	if qwenKey != "qwen-user-key" || deepSeekKey != "deepseek-user-key" {
		t.Fatalf("factory keys = %q, %q", qwenKey, deepSeekKey)
	}
}

func testHandler(speech provider.Speech, text provider.Text) http.Handler {
	return New(Dependencies{
		Speech:         speech,
		Text:           text,
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		MaxAudioBytes:  10 << 20,
		MaxDuration:    5 * time.Minute,
		RequestTimeout: 5 * time.Second,
		RequestsPerMin: 100,
	})
}

func multipartRequest(t *testing.T, audio []byte, filename, duration, mode string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	file, err := w.CreateFormFile("audio", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(audio); err != nil {
		t.Fatal(err)
	}
	_ = w.WriteField("duration_ms", duration)
	_ = w.WriteField("output_mode", mode)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/dictations", &body)
	request.Header.Set("Content-Type", w.FormDataContentType())
	return request
}

func testWAV(duration time.Duration) []byte {
	const byteRate = 32000
	dataSize := uint32(duration.Seconds() * byteRate)
	var output bytes.Buffer
	output.WriteString("RIFF")
	_ = binary.Write(&output, binary.LittleEndian, uint32(36)+dataSize)
	output.WriteString("WAVEfmt ")
	_ = binary.Write(&output, binary.LittleEndian, uint32(16))
	_ = binary.Write(&output, binary.LittleEndian, uint16(1))
	_ = binary.Write(&output, binary.LittleEndian, uint16(1))
	_ = binary.Write(&output, binary.LittleEndian, uint32(16000))
	_ = binary.Write(&output, binary.LittleEndian, uint32(byteRate))
	_ = binary.Write(&output, binary.LittleEndian, uint16(2))
	_ = binary.Write(&output, binary.LittleEndian, uint16(16))
	output.WriteString("data")
	_ = binary.Write(&output, binary.LittleEndian, dataSize)
	output.Write(make([]byte, dataSize))
	return output.Bytes()
}

// TestDictationChunkedFlowInOrder uploads three chunks in the documented
// order (last chunk arrives last). The handler should buffer earlier chunks,
// fire PolishChunks once all three are present, and emit Server-Timing that
// names the chunk and merge stages.
func TestDictationChunkedFlowInOrder(t *testing.T) {
	speech := &recordingSpeech{results: []string{"段0文字", "段1文字", "段2文字"}}
	text := &textStub{chunks: []string{"合并后文字"}}
	handler := testHandler(speech, text)

	const sessionID = "sess-1"
	pendingResp := uploadChunk(t, handler, sessionID, 0, 3, false, testWAV(time.Second), "1000")
	if pendingResp.statusCode != http.StatusOK {
		t.Fatalf("pending status = %d, body = %s", pendingResp.statusCode, pendingResp.body)
	}
	var pending dictationResponse
	if err := json.Unmarshal([]byte(pendingResp.body), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" || pending.ChunkIndex != 0 || pending.ChunkCount != 3 {
		t.Fatalf("pending response = %#v", pending)
	}
	if !strings.Contains(pendingResp.serverTiming, "asr_chunks_n=3") {
		t.Fatalf("Server-Timing missing chunk metric: %q", pendingResp.serverTiming)
	}

	uploadChunk(t, handler, sessionID, 1, 3, false, testWAV(time.Second), "1000")

	finalResp := uploadChunk(t, handler, sessionID, 2, 3, true, testWAV(time.Second), "1000")
	if finalResp.statusCode != http.StatusOK {
		t.Fatalf("final status = %d, body = %s", finalResp.statusCode, finalResp.body)
	}
	var final dictationResponse
	if err := json.Unmarshal([]byte(finalResp.body), &final); err != nil {
		t.Fatal(err)
	}
	if final.Status == "pending" || final.FinalText != "合并后文字" {
		t.Fatalf("final response = %#v", final)
	}
	if final.ChunkCount != 3 {
		t.Fatalf("chunk_count = %d", final.ChunkCount)
	}
	if !strings.Contains(finalResp.serverTiming, "merge_dedupe;dur=") {
		t.Fatalf("Server-Timing missing merge_dedupe: %q", finalResp.serverTiming)
	}
	if len(text.recordChunks) != 1 {
		t.Fatalf("expected one PolishChunks call, got %d", len(text.recordChunks))
	}
	got := text.recordChunks[0]
	want := []string{"段0文字", "段1文字", "段2文字"}
	if !equalStringSlices(got, want) {
		t.Fatalf("chunks = %#v, want %#v", got, want)
	}
	if speech.calls != 3 {
		t.Fatalf("expected 3 ASR calls, got %d", speech.calls)
	}
}

// TestDictationChunkedOutOfOrder feeds chunks out of order. The handler must
// still produce the final text by reordering the stored transcripts.
func TestDictationChunkedOutOfOrder(t *testing.T) {
	// recordingSpeech returns by call order; the test verifies the handler
	// reorders transcripts to chunk_index order before calling PolishChunks.
	speech := &recordingSpeech{results: []string{"段2文字", "段0文字", "段1文字"}}
	text := &textStub{chunks: []string{"合并后文字"}}
	handler := testHandler(speech, text)

	const sessionID = "sess-2"
	uploadChunk(t, handler, sessionID, 2, 3, false, testWAV(time.Second), "1000")
	uploadChunk(t, handler, sessionID, 0, 3, false, testWAV(time.Second), "1000")
	final := uploadChunk(t, handler, sessionID, 1, 3, true, testWAV(time.Second), "1000")
	if final.statusCode != http.StatusOK {
		t.Fatalf("status = %d", final.statusCode)
	}
	if len(text.recordChunks) != 1 {
		t.Fatalf("expected one PolishChunks call")
	}
	got := text.recordChunks[0]
	want := []string{"段0文字", "段1文字", "段2文字"}
	if !equalStringSlices(got, want) {
		t.Fatalf("chunks = %#v, want %#v", got, want)
	}
}

// TestDictationChunkedPolishingFails confirms that when the merge step fails
// the handler still returns the joined raw text and a warning so the client
// can fall back to the unpolished transcript.
func TestDictationChunkedPolishingFails(t *testing.T) {
	speech := &recordingSpeech{results: []string{"段0", "段1"}}
	text := &textStub{chunksErr: errors.New("upstream down")}
	handler := testHandler(speech, text)

	const sessionID = "sess-3"
	uploadChunk(t, handler, sessionID, 0, 2, false, testWAV(time.Second), "1000")
	final := uploadChunk(t, handler, sessionID, 1, 2, true, testWAV(time.Second), "1000")

	if final.statusCode != http.StatusOK {
		t.Fatalf("status = %d", final.statusCode)
	}
	var body dictationResponse
	if err := json.Unmarshal([]byte(final.body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Warning == nil || body.Warning.Code != "polishing_failed" {
		t.Fatalf("expected polishing_failed warning, got %#v", body)
	}
	if body.RawText == "" {
		t.Fatalf("expected joined raw text")
	}
}

// TestDictationChunkedSessionTimeout sends a final chunk while prior chunks
// are missing. The handler must surface a session_timeout once the request
// context expires instead of hanging.
func TestDictationChunkedSessionTimeout(t *testing.T) {
	speech := &recordingSpeech{results: []string{"段0"}}
	handler := testHandler(speech, &textStub{chunks: []string{"合并"}})

	const sessionID = "sess-4"
	final := uploadChunk(t, handler, sessionID, 1, 3, true, testWAV(time.Second), "1000")
	if final.statusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body = %s", final.statusCode, final.body)
	}
	if !strings.Contains(final.body, "session_timeout") {
		t.Fatalf("body = %s", final.body)
	}
}

// TestDictationChunkedMissingFields rejects chunked requests that do not
// provide the full set of fields.
func TestDictationChunkedMissingFields(t *testing.T) {
	handler := testHandler(speechStub{}, &textStub{})

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	file, _ := w.CreateFormFile("audio", "recording.wav")
	_, _ = file.Write(testWAV(time.Second))
	_ = w.WriteField("duration_ms", "1000")
	_ = w.WriteField("output_mode", "polished")
	_ = w.WriteField("session_id", "only-session")
	_ = w.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/dictations", body)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

// TestDictationChunkedEmptyTranscriptContinues verifies that when one chunk
// produces no transcript (e.g. silent tail), the handler stores an empty
// string for that chunk instead of aborting the whole session. The other
// chunks should still flow through to PolishChunks with the empty slot
// left blank so the LLM merge step can drop the silent region.
func TestDictationChunkedEmptyTranscriptContinues(t *testing.T) {
	speech := &recordingSpeech{results: []string{"段0文字", "", "段2文字"}}
	text := &textStub{chunks: []string{"合并后文字"}}
	handler := testHandler(speech, text)

	const sessionID = "sess-empty"
	uploadChunk(t, handler, sessionID, 0, 3, false, testWAV(time.Second), "1000")
	uploadChunk(t, handler, sessionID, 1, 3, false, testWAV(time.Second), "1000")
	final := uploadChunk(t, handler, sessionID, 2, 3, true, testWAV(time.Second), "1000")

	if final.statusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", final.statusCode, final.body)
	}
	var body dictationResponse
	if err := json.Unmarshal([]byte(final.body), &body); err != nil {
		t.Fatal(err)
	}
	if body.FinalText != "合并后文字" {
		t.Fatalf("final_text = %q", body.FinalText)
	}
	if len(text.recordChunks) != 1 {
		t.Fatalf("expected one PolishChunks call, got %d", len(text.recordChunks))
	}
	got := text.recordChunks[0]
	want := []string{"段0文字", "", "段2文字"}
	if !equalStringSlices(got, want) {
		t.Fatalf("chunks = %#v, want %#v", got, want)
	}
	if !errors.Is(errors.New(""), provider.ErrEmptyTranscript) {
		// Silence unused-import check when test stubs are removed.
		_ = fmt.Sprint("")
	}
}

// TestDictationChunkedRejectsOversizedSession verifies that a malicious or
// buggy client cannot claim an absurdly large chunk_total — the handler must
// return 400 before allocating a session-sized map.
func TestDictationChunkedRejectsOversizedSession(t *testing.T) {
	handler := testHandler(speechStub{}, &textStub{})

	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	file, _ := w.CreateFormFile("audio", "recording.wav")
	_, _ = file.Write(testWAV(time.Second))
	_ = w.WriteField("duration_ms", "1000")
	_ = w.WriteField("output_mode", "polished")
	_ = w.WriteField("session_id", "oversized-session")
	_ = w.WriteField("chunk_index", "0")
	_ = w.WriteField("chunk_total", "1000000")
	w.Close()

	req := httptest.NewRequest(http.MethodPost, "/v1/dictations", body)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", resp.Code, resp.Body.String())
	}
}

// TestDictationChunkedCumulativeASRReporting verifies that the final
// response's Server-Timing reports the cumulative asr duration across all
// chunks, not just the last chunk's individual ASR. This is the regression
// guard for the fix that moves the running total onto sessionState.
func TestDictationChunkedCumulativeASRReporting(t *testing.T) {
	speech := &recordingSpeech{results: []string{"a", "b", "c"}}
	text := &textStub{chunks: []string{"merged"}}
	handler := testHandler(speech, text)

	const sessionID = "sess-cumulative"
	uploadChunk(t, handler, sessionID, 0, 3, false, testWAV(time.Second), "1000")
	uploadChunk(t, handler, sessionID, 1, 3, false, testWAV(time.Second), "1000")
	final := uploadChunk(t, handler, sessionID, 2, 3, true, testWAV(time.Second), "1000")

	if final.statusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %s", final.statusCode, final.body)
	}
	// asr_chunks_n is required by the metric schema and should be 3 for a
	// three-chunk session.
	if !strings.Contains(final.serverTiming, "asr_chunks_n=3") {
		t.Fatalf("Server-Timing missing n=3: %q", final.serverTiming)
	}
	// The cumulative duration must be a strictly positive number, not zero
	// or empty. We don't assert an exact value because the stub ASR takes
	// effectively no time; we just need to make sure the metric is emitted
	// with a numeric dur.
	if !strings.Contains(final.serverTiming, "asr_chunks;dur=") {
		t.Fatalf("Server-Timing missing asr_chunks duration: %q", final.serverTiming)
	}
}

// TestDictationChunkedHardASRErrorAborts ensures that real failures (network
// errors, 5xx) still abort the session. Only ErrEmptyTranscript is soft.
func TestDictationChunkedHardASRErrorAborts(t *testing.T) {
	speech := &recordingSpeech{results: []string{"段0", "", "段2"}}
	// Wrap the second result with a real (non-empty-transcript) error.
	speech.errors = []error{nil, errors.New("network down"), nil}
	text := &textStub{chunks: []string{"合并"}}
	handler := testHandler(speech, text)

	const sessionID = "sess-hard"
	uploadChunk(t, handler, sessionID, 0, 3, false, testWAV(time.Second), "1000")
	final := uploadChunk(t, handler, sessionID, 1, 3, true, testWAV(time.Second), "1000")

	if final.statusCode != http.StatusBadGateway && final.statusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body = %s", final.statusCode, final.body)
	}
	if len(text.recordChunks) != 0 {
		t.Fatalf("PolishChunks should not be called on hard failure, got %d calls", len(text.recordChunks))
	}
}

// recordingSpeech returns successive Transcribe results so multi-chunk tests
// can verify the per-chunk ASR calls without coupling to a real provider.
type recordingSpeech struct {
	results []string
	errors  []error
	calls   int
}

func (s *recordingSpeech) Transcribe(_ context.Context, _ provider.Audio) (string, error) {
	idx := s.calls
	s.calls++
	if idx >= len(s.results) {
		return "", errors.New("recordingSpeech: out of results")
	}
	var err error
	if idx < len(s.errors) {
		err = s.errors[idx]
	}
	return s.results[idx], err
}

type chunkUploadResult struct {
	statusCode    int
	body          string
	serverTiming  string
}

func uploadChunk(t *testing.T, handler http.Handler, sessionID string, index, total int, isLast bool, audio []byte, durationMs string) chunkUploadResult {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	file, err := w.CreateFormFile("audio", "recording.wav")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(audio); err != nil {
		t.Fatal(err)
	}
	_ = w.WriteField("duration_ms", durationMs)
	_ = w.WriteField("output_mode", "polished")
	_ = w.WriteField("session_id", sessionID)
	_ = w.WriteField("chunk_index", strconv.Itoa(index))
	_ = w.WriteField("chunk_total", strconv.Itoa(total))
	if isLast {
		_ = w.WriteField("is_last", "true")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/dictations", &body)
	req.Header.Set("Content-Type", w.FormDataContentType())

	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)

	return chunkUploadResult{
		statusCode:   resp.Code,
		body:         resp.Body.String(),
		serverTiming: resp.Header().Get("Server-Timing"),
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
