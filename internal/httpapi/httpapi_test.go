package httpapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
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
	text string
	err  error
}

func (s textStub) Polish(_ context.Context, _ string) (string, error) {
	return s.text, s.err
}

func TestDictationSuccess(t *testing.T) {
	handler := testHandler(speechStub{text: "原始文字"}, textStub{text: "最终文字"})
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
	handler := testHandler(speechStub{text: "原始文字"}, textStub{err: errors.New("unavailable")})
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
	handler := testHandler(speechStub{text: "should not be returned"}, textStub{})
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
		text:   textStub{text: "default text"},
		speechForKey: func(key string) provider.Speech {
			qwenKey = key
			return speechStub{text: "custom speech"}
		},
		textForKey: func(key string) provider.Text {
			deepSeekKey = key
			return textStub{text: "custom text"}
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
