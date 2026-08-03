package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"typezero/internal/audioinfo"
	"typezero/internal/provider"
)

type Dependencies struct {
	Speech           provider.Speech
	Text             provider.Text
	SpeechForKey     func(string) provider.Speech
	TextForKey       func(string) provider.Text
	Logger           *slog.Logger
	MaxAudioBytes    int64
	MaxDuration      time.Duration
	RequestTimeout   time.Duration
	RequestsPerMin   int
	TrustedProxyCIDR string
	SessionTTL       time.Duration
}

type API struct {
	speech         provider.Speech
	text           provider.Text
	speechForKey   func(string) provider.Speech
	textForKey     func(string) provider.Text
	logger         *slog.Logger
	maxAudioBytes  int64
	maxDuration    time.Duration
	requestTimeout time.Duration
	limiter        *rateLimiter
	trustedProxy   *net.IPNet
	sessions       *SessionStore
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// dictationResponse is the unified response shape used both for the legacy
// single-shot dictation and for the final chunk of a chunked session. When
// status is "pending" the response is an interim acknowledgement for a
// non-final chunk and final_text is empty.
type dictationResponse struct {
	RequestID  string   `json:"request_id"`
	SessionID  string   `json:"session_id,omitempty"`
	ChunkIndex int      `json:"chunk_index,omitempty"`
	ChunkCount int      `json:"chunk_count,omitempty"`
	Status     string   `json:"status,omitempty"`
	RawText    string   `json:"raw_text"`
	FinalText  string   `json:"final_text,omitempty"`
	Warning    *warning `json:"warning,omitempty"`
}

// requestTimings captures elapsed durations per pipeline stage. Field order
// in the Server-Timing header is stable so clients can rely on names.
type requestTimings struct {
	intake         time.Duration
	asr            time.Duration
	chunkAsrTotal  time.Duration
	chunkAsrMax    time.Duration
	mergeDedupe    time.Duration
	polish         time.Duration
	asrRan         bool
	polishRan      bool
	chunkAsrCounts int
}

func (t requestTimings) serverTiming() string {
	parts := []string{fmt.Sprintf("intake;dur=%.3f", durationMilliseconds(t.intake))}
	if t.chunkAsrCounts > 0 {
		parts = append(parts,
			fmt.Sprintf("asr_chunks;dur=%.3f", durationMilliseconds(t.chunkAsrTotal)),
			fmt.Sprintf("asr_chunks_max;dur=%.3f", durationMilliseconds(t.chunkAsrMax)),
			fmt.Sprintf("asr_chunks_n=%d", t.chunkAsrCounts),
		)
	} else if t.asrRan {
		parts = append(parts, fmt.Sprintf("asr;dur=%.3f", durationMilliseconds(t.asr)))
	}
	if t.mergeDedupe > 0 {
		parts = append(parts, fmt.Sprintf("merge_dedupe;dur=%.3f", durationMilliseconds(t.mergeDedupe)))
	}
	if t.polishRan {
		parts = append(parts, fmt.Sprintf("polish;dur=%.3f", durationMilliseconds(t.polish)))
	}
	return strings.Join(parts, ", ")
}

func durationMilliseconds(value time.Duration) float64 {
	return float64(value.Microseconds()) / 1000
}

func New(deps Dependencies) http.Handler {
	api := &API{
		speech:         deps.Speech,
		text:           deps.Text,
		speechForKey:   deps.SpeechForKey,
		textForKey:     deps.TextForKey,
		logger:         deps.Logger,
		maxAudioBytes:  deps.MaxAudioBytes,
		maxDuration:    deps.MaxDuration,
		requestTimeout: deps.RequestTimeout,
		limiter:        newRateLimiter(deps.RequestsPerMin),
		sessions:       newSessionStore(deps.SessionTTL),
	}
	if deps.TrustedProxyCIDR != "" {
		_, api.trustedProxy, _ = net.ParseCIDR(deps.TrustedProxyCIDR)
	}

	// The session janitor runs until the process exits. It uses
	// context.Background so we don't have to plumb shutdown through the
	// http.Handler lifecycle.
	go api.sessions.runJanitor(context.Background(), 0)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	mux.HandleFunc("POST /v1/dictations", api.dictations)
	return securityHeaders(mux)
}

func (a *API) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (a *API) dictations(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	requestID := newRequestID()
	w.Header().Set("X-Request-ID", requestID)
	timings := requestTimings{}

	if !a.limiter.allow(a.clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		a.writeTimingHeader(w, &timings)
		a.fail(w, http.StatusTooManyRequests, "rate_limit_exceeded", "请求过于频繁，请稍后重试")
		a.log(requestID, "", 0, started, timings, "rate_limited", nil)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, a.maxAudioBytes+(1<<20))
	intakeStarted := time.Now()

	if err := r.ParseMultipartForm(a.maxAudioBytes); err != nil {
		timings.intake = time.Since(intakeStarted)
		status := http.StatusBadRequest
		code := "invalid_multipart"
		message := "请求必须是有效的 multipart/form-data"
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status, code, message = http.StatusRequestEntityTooLarge, "audio_too_large", "音频不得超过 10 MB"
		}
		a.writeTimingHeader(w, &timings)
		a.fail(w, status, code, message)
		a.log(requestID, "", 0, started, timings, code, err)
		return
	}
	defer r.MultipartForm.RemoveAll()

	mode := strings.TrimSpace(r.FormValue("output_mode"))
	if mode == "" {
		mode = "polished"
	}
	if mode != "polished" && mode != "raw" {
		timings.intake = time.Since(intakeStarted)
		a.writeTimingHeader(w, &timings)
		a.fail(w, http.StatusBadRequest, "invalid_output_mode", "output_mode 仅支持 polished 或 raw")
		a.log(requestID, "", 0, started, timings, "invalid_output_mode", nil)
		return
	}

	audio, info, err := a.readAudio(r)
	if err != nil {
		timings.intake = time.Since(intakeStarted)
		var clientErr *clientError
		if errors.As(err, &clientErr) {
			a.writeTimingHeader(w, &timings)
			a.fail(w, clientErr.status, clientErr.code, clientErr.message)
			a.log(requestID, "", 0, started, timings, clientErr.code, err)
			return
		}
		a.writeTimingHeader(w, &timings)
		a.fail(w, http.StatusInternalServerError, "internal_error", "服务暂时不可用")
		a.log(requestID, "", 0, started, timings, "internal_error", err)
		return
	}
	if info.Duration > a.maxDuration {
		timings.intake = time.Since(intakeStarted)
		a.writeTimingHeader(w, &timings)
		a.fail(w, http.StatusUnprocessableEntity, "audio_too_long", "音频不得超过 5 分钟")
		a.log(requestID, "", 0, started, timings, "audio_too_long", nil)
		return
	}

	declaredDuration, err := parseDeclaredDuration(r.FormValue("duration_ms"))
	if err != nil {
		timings.intake = time.Since(intakeStarted)
		a.writeTimingHeader(w, &timings)
		a.fail(w, http.StatusBadRequest, "invalid_duration", "duration_ms 必须是正整数")
		a.log(requestID, "", 0, started, timings, "invalid_duration", err)
		return
	}
	if declaredDuration > a.maxDuration {
		timings.intake = time.Since(intakeStarted)
		a.writeTimingHeader(w, &timings)
		a.fail(w, http.StatusUnprocessableEntity, "audio_too_long", "音频不得超过 5 分钟")
		a.log(requestID, "", 0, started, timings, "audio_too_long", nil)
		return
	}

	speech, text, err := a.providersForRequest(r)
	if err != nil {
		timings.intake = time.Since(intakeStarted)
		a.writeTimingHeader(w, &timings)
		a.fail(w, http.StatusBadRequest, "invalid_api_key", "模型 API Key 格式无效")
		a.log(requestID, "", 0, started, timings, "invalid_api_key", nil)
		return
	}
	timings.intake = time.Since(intakeStarted)

	// Chunked mode is triggered when the client provides session_id and
	// chunk_index. chunk_total is also required. Anything missing falls back
	// to the legacy single-shot path so existing clients keep working.
	sessionID, chunkIndex, chunkTotal, isLast, chunked, err := a.parseChunkFields(r)
	if err != nil {
		a.writeTimingHeader(w, &timings)
		a.fail(w, http.StatusBadRequest, err.Error(), "分段参数无效")
		a.log(requestID, sessionID, chunkTotal, started, timings, err.Error(), nil)
		return
	}

	if chunked {
		a.handleChunked(w, r, ctx, requestID, sessionID, chunkIndex, chunkTotal, isLast, audio, speech, text, mode, started, &timings)
		return
	}

	a.handleSingle(w, r, ctx, requestID, audio, speech, text, mode, started, &timings)
}

func (a *API) handleSingle(w http.ResponseWriter, r *http.Request, ctx context.Context, requestID string, audio provider.Audio, speech provider.Speech, text provider.Text, mode string, started time.Time, timings *requestTimings) {
	timings.asrRan = true
	asrStarted := time.Now()
	rawText, err := speech.Transcribe(ctx, audio)
	timings.asr = time.Since(asrStarted)
	if err != nil {
		status, code := providerFailure(ctx, "transcription_failed")
		a.writeTimingHeader(w, timings)
		a.fail(w, status, code, "语音识别失败，请重试")
		a.log(requestID, "", 0, started, *timings, code, err)
		return
	}
	if mode == "raw" {
		a.writeTimingHeader(w, timings)
		writeJSON(w, http.StatusOK, dictationResponse{RequestID: requestID, RawText: rawText, FinalText: rawText})
		a.log(requestID, "", 0, started, *timings, "ok_raw", nil)
		return
	}

	timings.polishRan = true
	polishStarted := time.Now()
	finalText, err := text.Polish(ctx, rawText)
	timings.polish = time.Since(polishStarted)
	if err != nil {
		response := dictationResponse{
			RequestID: requestID,
			RawText:   rawText,
			Warning: &warning{
				Code:    "polishing_failed",
				Message: "润色失败，可使用原始识别文字",
			},
		}
		a.writeTimingHeader(w, timings)
		writeJSON(w, http.StatusOK, response)
		a.log(requestID, "", 0, started, *timings, "polishing_failed", err)
		return
	}

	a.writeTimingHeader(w, timings)
	writeJSON(w, http.StatusOK, dictationResponse{RequestID: requestID, RawText: rawText, FinalText: finalText})
	a.log(requestID, "", 0, started, *timings, "ok", nil)
}

func (a *API) handleChunked(w http.ResponseWriter, r *http.Request, ctx context.Context, requestID, sessionID string, chunkIndex, chunkTotal int, isLast bool, audio provider.Audio, speech provider.Speech, text provider.Text, mode string, started time.Time, timings *requestTimings) {
	state, _, err := a.sessions.getOrCreate(sessionID, chunkTotal)
	if err != nil {
		a.writeTimingHeader(w, timings)
		a.fail(w, http.StatusBadRequest, err.Error(), "分段参数无效")
		a.log(requestID, sessionID, chunkTotal, started, *timings, err.Error(), nil)
		return
	}

	asrStarted := time.Now()
	rawText, asrErr := speech.Transcribe(ctx, audio)
	asrElapsed := time.Since(asrStarted)

	timings.chunkAsrCounts = chunkTotal
	timings.chunkAsrTotal += asrElapsed
	if asrElapsed > timings.chunkAsrMax {
		timings.chunkAsrMax = asrElapsed
	}
	timings.asrRan = true

	if asrErr != nil {
		// Remove the session so the client can retry the whole sequence.
		a.sessions.remove(sessionID)
		status, code := providerFailure(ctx, "transcription_failed")
		a.writeTimingHeader(w, timings)
		a.fail(w, status, code, "语音识别失败，请重试")
		a.log(requestID, sessionID, chunkTotal, started, *timings, code, asrErr)
		return
	}

	complete := state.store(chunkIndex, rawText, time.Now())

	if !isLast {
		a.writeTimingHeader(w, timings)
		writeJSON(w, http.StatusOK, dictationResponse{
			RequestID:  requestID,
			SessionID:  sessionID,
			ChunkIndex: chunkIndex,
			ChunkCount: chunkTotal,
			Status:     "pending",
			RawText:    rawText,
		})
		a.log(requestID, sessionID, chunkTotal, started, *timings, "chunk_pending", nil)
		return
	}

	if !complete {
		// Other chunks haven't arrived yet. Wait for them or for ctx to expire.
		all, waitErr := a.sessions.waitForCompletion(ctx, sessionID, 10*time.Millisecond)
		if waitErr != nil {
			a.sessions.remove(sessionID)
			a.writeTimingHeader(w, timings)
			if errors.Is(waitErr, context.DeadlineExceeded) {
				a.fail(w, http.StatusGatewayTimeout, "session_timeout", "等待其他分段超时")
				a.log(requestID, sessionID, chunkTotal, started, *timings, "session_timeout", waitErr)
				return
			}
			a.fail(w, http.StatusBadRequest, waitErr.Error(), "会话状态异常")
			a.log(requestID, sessionID, chunkTotal, started, *timings, "session_error", waitErr)
			return
		}
		// Drop this final chunk's text (already stored) into the snapshot.
		all[chunkIndex] = rawText
		a.mergeAndPolish(w, ctx, requestID, sessionID, chunkTotal, all, text, mode, started, timings)
		return
	}

	all := state.snapshot()
	a.mergeAndPolish(w, ctx, requestID, sessionID, chunkTotal, all, text, mode, started, timings)
}

func (a *API) mergeAndPolish(w http.ResponseWriter, ctx context.Context, requestID, sessionID string, chunkTotal int, chunks []string, text provider.Text, mode string, started time.Time, timings *requestTimings) {
	// Tidy: cleanup session regardless of outcome.
	defer a.sessions.remove(sessionID)

	joined := strings.Join(chunks, "\n")
	if mode == "raw" {
		a.writeTimingHeader(w, timings)
		writeJSON(w, http.StatusOK, dictationResponse{
			RequestID:  requestID,
			SessionID:  sessionID,
			ChunkCount: chunkTotal,
			RawText:    joined,
			FinalText:  joined,
		})
		a.log(requestID, sessionID, chunkTotal, started, *timings, "ok_chunked_raw", nil)
		return
	}

	timings.polishRan = true
	polishStarted := time.Now()
	finalText, err := text.PolishChunks(ctx, chunks)
	timings.polish = time.Since(polishStarted)
	// merge_dedupe is charged the entire polish time for chunks: the model
	// performs both merge and polish in one pass, so splitting them is not
	// observable from outside. We still keep the metric name to align with
	// the documented stage list.
	timings.mergeDedupe = timings.polish

	if err != nil {
		a.writeTimingHeader(w, timings)
		writeJSON(w, http.StatusOK, dictationResponse{
			RequestID:  requestID,
			SessionID:  sessionID,
			ChunkCount: chunkTotal,
			RawText:    joined,
			Warning: &warning{
				Code:    "polishing_failed",
				Message: "润色失败，可使用原始识别文字",
			},
		})
		a.log(requestID, sessionID, chunkTotal, started, *timings, "polishing_failed", err)
		return
	}

	a.writeTimingHeader(w, timings)
	writeJSON(w, http.StatusOK, dictationResponse{
		RequestID:  requestID,
		SessionID:  sessionID,
		ChunkCount: chunkTotal,
		RawText:    joined,
		FinalText:  finalText,
	})
	a.log(requestID, sessionID, chunkTotal, started, *timings, "ok_chunked", nil)
}

// parseChunkFields extracts session_id / chunk_index / chunk_total / is_last
// from the multipart form. Returns chunked=true only when all four fields are
// present and well-formed.
func (a *API) parseChunkFields(r *http.Request) (sessionID string, chunkIndex, chunkTotal int, isLast bool, chunked bool, err error) {
	sessionID = strings.TrimSpace(r.FormValue("session_id"))
	if sessionID == "" {
		return "", 0, 0, false, false, nil
	}
	if len(sessionID) > 128 {
		return "", 0, 0, false, true, errors.New("invalid_session_id")
	}
	idxRaw := strings.TrimSpace(r.FormValue("chunk_index"))
	totalRaw := strings.TrimSpace(r.FormValue("chunk_total"))
	lastRaw := strings.TrimSpace(r.FormValue("is_last"))
	if idxRaw == "" || totalRaw == "" {
		return "", 0, 0, false, true, errors.New("missing_chunk_fields")
	}
	idx, err := strconv.Atoi(idxRaw)
	if err != nil || idx < 0 {
		return "", 0, 0, false, true, errors.New("invalid_chunk_index")
	}
	total, err := strconv.Atoi(totalRaw)
	if err != nil || total <= 0 {
		return "", 0, 0, false, true, errors.New("invalid_chunk_total")
	}
	if idx >= total {
		return "", 0, 0, false, true, errors.New("invalid_chunk_index")
	}
	switch strings.ToLower(lastRaw) {
	case "1", "true", "yes":
		isLast = true
	case "", "0", "false", "no":
		isLast = false
	default:
		return "", 0, 0, false, true, errors.New("invalid_is_last")
	}
	return sessionID, idx, total, isLast, true, nil
}

func (a *API) providersForRequest(r *http.Request) (provider.Speech, provider.Text, error) {
	const maxKeyLength = 512
	speech := a.speech
	text := a.text
	qwenKey := strings.TrimSpace(r.Header.Get("X-TypeZero-DashScope-Key"))
	deepSeekKey := strings.TrimSpace(r.Header.Get("X-TypeZero-DeepSeek-Key"))
	if len(qwenKey) > maxKeyLength || len(deepSeekKey) > maxKeyLength {
		return nil, nil, errors.New("API key exceeds maximum length")
	}
	if qwenKey != "" && a.speechForKey != nil {
		speech = a.speechForKey(qwenKey)
	}
	if deepSeekKey != "" && a.textForKey != nil {
		text = a.textForKey(deepSeekKey)
	}
	return speech, text, nil
}

type clientError struct {
	status  int
	code    string
	message string
}

func (e *clientError) Error() string { return e.code }

func (a *API) readAudio(r *http.Request) (provider.Audio, audioinfo.Info, error) {
	file, header, err := r.FormFile("audio")
	if err != nil {
		if errors.Is(err, http.ErrMissingFile) {
			return provider.Audio{}, audioinfo.Info{}, &clientError{http.StatusBadRequest, "audio_required", "缺少 audio 文件"}
		}
		return provider.Audio{}, audioinfo.Info{}, err
	}
	defer file.Close()
	if header.Size > a.maxAudioBytes {
		return provider.Audio{}, audioinfo.Info{}, &clientError{http.StatusRequestEntityTooLarge, "audio_too_large", "音频不得超过 10 MB"}
	}

	data, err := io.ReadAll(io.LimitReader(file, a.maxAudioBytes+1))
	if err != nil {
		return provider.Audio{}, audioinfo.Info{}, fmt.Errorf("read audio: %w", err)
	}
	if int64(len(data)) > a.maxAudioBytes {
		return provider.Audio{}, audioinfo.Info{}, &clientError{http.StatusRequestEntityTooLarge, "audio_too_large", "音频不得超过 10 MB"}
	}
	if len(data) == 0 {
		return provider.Audio{}, audioinfo.Info{}, &clientError{http.StatusBadRequest, "audio_empty", "音频文件不能为空"}
	}

	info, err := audioinfo.Inspect(data, header.Filename)
	if err != nil {
		if errors.Is(err, audioinfo.ErrUnsupported) {
			return provider.Audio{}, audioinfo.Info{}, &clientError{http.StatusUnsupportedMediaType, "unsupported_audio", "仅支持 M4A/MP4(AAC) 或 WAV 音频"}
		}
		return provider.Audio{}, audioinfo.Info{}, &clientError{http.StatusUnprocessableEntity, "invalid_audio", "音频文件已损坏或缺少时长信息"}
	}
	if info.MediaType != "audio/wav" {
		return provider.Audio{}, audioinfo.Info{}, &clientError{http.StatusUnsupportedMediaType, "unsupported_audio", "当前识别模型仅支持 WAV 音频"}
	}
	return provider.Audio{Data: data, MediaType: info.MediaType, Filename: header.Filename}, info, nil
}

func parseDeclaredDuration(value string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, errors.New("duration_ms is required")
	}
	milliseconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || milliseconds <= 0 {
		return 0, errors.New("duration_ms must be positive")
	}
	if milliseconds > int64(^uint64(0)>>1)/int64(time.Millisecond) {
		return 0, errors.New("duration_ms is too large")
	}
	return time.Duration(milliseconds) * time.Millisecond, nil
}

func providerFailure(ctx context.Context, fallback string) (int, string) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return http.StatusGatewayTimeout, "processing_timeout"
	}
	return http.StatusBadGateway, fallback
}

func (a *API) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remoteIP := net.ParseIP(host)
	if a.trustedProxy != nil && remoteIP != nil && a.trustedProxy.Contains(remoteIP) {
		if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
			return forwarded
		}
	}
	return host
}

func (a *API) fail(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: message}})
}

func (a *API) writeTimingHeader(w http.ResponseWriter, timings *requestTimings) {
	w.Header().Set("Server-Timing", timings.serverTiming())
}

func (a *API) log(requestID, sessionID string, chunkCount int, started time.Time, timings requestTimings, outcome string, err error) {
	attrs := []any{
		"request_id", requestID,
		"duration_ms", time.Since(started).Milliseconds(),
		"intake_ms", timings.intake.Milliseconds(),
		"outcome", outcome,
	}
	if sessionID != "" {
		attrs = append(attrs, "session_id", sessionID, "chunk_count", chunkCount)
	}
	if timings.chunkAsrCounts > 0 {
		attrs = append(attrs,
			"asr_ms_total", timings.chunkAsrTotal.Milliseconds(),
			"asr_ms_max", timings.chunkAsrMax.Milliseconds(),
			"asr_chunks", timings.chunkAsrCounts,
		)
	} else if timings.asrRan {
		attrs = append(attrs, "asr_ms", timings.asr.Milliseconds())
	}
	if timings.mergeDedupe > 0 {
		attrs = append(attrs, "merge_dedupe_ms", timings.mergeDedupe.Milliseconds())
	}
	if timings.polishRan {
		attrs = append(attrs, "polish_ms", timings.polish.Milliseconds())
	}
	if err != nil {
		attrs = append(attrs, "error", err.Error())
	}
	a.logger.Info("dictation request completed", attrs...)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func newRequestID() string {
	bytes := make([]byte, 12)
	if _, err := rand.Read(bytes); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(bytes)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}