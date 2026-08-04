package groq

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"typezero/internal/provider"
)

func TestTranscribe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		if r.URL.Path != "/audio/transcriptions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if !strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		_ = r.ParseMultipartForm(10 << 20)
		if got := r.FormValue("model"); got != "whisper-large-v3" {
			t.Errorf("model = %q", got)
		}
		file, _, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("file part: %v", err)
		}
		defer file.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "  你好，世界。  "})
	}))
	defer server.Close()

	client := New(server.Client(), server.URL, "secret", "whisper-large-v3")
	got, err := client.Transcribe(context.Background(), provider.Audio{Data: []byte{1, 2, 3}, MediaType: "audio/wav"})
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}
	if got != "你好，世界。" {
		t.Fatalf("Transcribe() = %q", got)
	}
}

func TestTranscribeRetriesRateLimit(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error","code":"rate_limit_exceeded"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"text":"重试成功"}`))
	}))
	defer server.Close()

	client := New(server.Client(), server.URL, "secret", "whisper-large-v3")
	got, err := client.Transcribe(context.Background(), provider.Audio{Data: []byte{1}, MediaType: "audio/wav"})
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}
	if got != "重试成功" {
		t.Fatalf("Transcribe() = %q", got)
	}
	if calls != 2 {
		t.Fatalf("expected 2 attempts, got %d", calls)
	}
}

func TestTranscribeEmptyIsSoftFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"text":"   "}`))
	}))
	defer server.Close()

	client := New(server.Client(), server.URL, "secret", "whisper-large-v3")
	_, err := client.Transcribe(context.Background(), provider.Audio{Data: []byte{1}, MediaType: "audio/wav"})
	if err != provider.ErrEmptyTranscript {
		t.Fatalf("Transcribe() error = %v, want ErrEmptyTranscript", err)
	}
}

func TestTranscribeTimeoutNoRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	client := New(&http.Client{Timeout: 50 * time.Millisecond}, server.URL, "secret", "whisper-large-v3")
	_, err := client.Transcribe(context.Background(), provider.Audio{Data: []byte{1}, MediaType: "audio/wav"})
	if err == nil {
		t.Fatal("Transcribe() error = nil, want timeout")
	}
	if calls != 1 {
		t.Fatalf("expected 1 attempt (no retry on timeout), got %d", calls)
	}
}
