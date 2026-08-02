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
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type dictationResponse struct {
	RequestID string   `json:"request_id"`
	RawText   string   `json:"raw_text"`
	FinalText string   `json:"final_text"`
	Warning   *warning `json:"warning,omitempty"`
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
	}
	if deps.TrustedProxyCIDR != "" {
		_, api.trustedProxy, _ = net.ParseCIDR(deps.TrustedProxyCIDR)
	}

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

	if !a.limiter.allow(a.clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		a.fail(w, http.StatusTooManyRequests, "rate_limit_exceeded", "请求过于频繁，请稍后重试")
		a.log(requestID, started, "rate_limited", nil)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), a.requestTimeout)
	defer cancel()
	r = r.WithContext(ctx)
	r.Body = http.MaxBytesReader(w, r.Body, a.maxAudioBytes+(1<<20))

	if err := r.ParseMultipartForm(a.maxAudioBytes); err != nil {
		status := http.StatusBadRequest
		code := "invalid_multipart"
		message := "请求必须是有效的 multipart/form-data"
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status, code, message = http.StatusRequestEntityTooLarge, "audio_too_large", "音频不得超过 10 MB"
		}
		a.fail(w, status, code, message)
		a.log(requestID, started, code, err)
		return
	}
	defer r.MultipartForm.RemoveAll()

	mode := strings.TrimSpace(r.FormValue("output_mode"))
	if mode == "" {
		mode = "polished"
	}
	if mode != "polished" && mode != "raw" {
		a.fail(w, http.StatusBadRequest, "invalid_output_mode", "output_mode 仅支持 polished 或 raw")
		a.log(requestID, started, "invalid_output_mode", nil)
		return
	}

	audio, info, err := a.readAudio(r)
	if err != nil {
		var clientErr *clientError
		if errors.As(err, &clientErr) {
			a.fail(w, clientErr.status, clientErr.code, clientErr.message)
			a.log(requestID, started, clientErr.code, err)
			return
		}
		a.fail(w, http.StatusInternalServerError, "internal_error", "服务暂时不可用")
		a.log(requestID, started, "internal_error", err)
		return
	}
	if info.Duration > a.maxDuration {
		a.fail(w, http.StatusUnprocessableEntity, "audio_too_long", "音频不得超过 5 分钟")
		a.log(requestID, started, "audio_too_long", nil)
		return
	}

	declaredDuration, err := parseDeclaredDuration(r.FormValue("duration_ms"))
	if err != nil {
		a.fail(w, http.StatusBadRequest, "invalid_duration", "duration_ms 必须是正整数")
		a.log(requestID, started, "invalid_duration", err)
		return
	}
	if declaredDuration > a.maxDuration {
		a.fail(w, http.StatusUnprocessableEntity, "audio_too_long", "音频不得超过 5 分钟")
		a.log(requestID, started, "audio_too_long", nil)
		return
	}

	speech, text, err := a.providersForRequest(r)
	if err != nil {
		a.fail(w, http.StatusBadRequest, "invalid_api_key", "模型 API Key 格式无效")
		a.log(requestID, started, "invalid_api_key", nil)
		return
	}

	rawText, err := speech.Transcribe(ctx, audio)
	if err != nil {
		status, code := providerFailure(ctx, "transcription_failed")
		a.fail(w, status, code, "语音识别失败，请重试")
		a.log(requestID, started, code, err)
		return
	}
	if mode == "raw" {
		writeJSON(w, http.StatusOK, dictationResponse{RequestID: requestID, RawText: rawText, FinalText: rawText})
		a.log(requestID, started, "ok_raw", nil)
		return
	}

	finalText, err := text.Polish(ctx, rawText)
	if err != nil {
		response := dictationResponse{
			RequestID: requestID,
			RawText:   rawText,
			Warning: &warning{
				Code:    "polishing_failed",
				Message: "润色失败，可使用原始识别文字",
			},
		}
		writeJSON(w, http.StatusOK, response)
		a.log(requestID, started, "polishing_failed", err)
		return
	}

	writeJSON(w, http.StatusOK, dictationResponse{RequestID: requestID, RawText: rawText, FinalText: finalText})
	a.log(requestID, started, "ok", nil)
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

func (a *API) log(requestID string, started time.Time, outcome string, err error) {
	attrs := []any{"request_id", requestID, "duration_ms", time.Since(started).Milliseconds(), "outcome", outcome}
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
