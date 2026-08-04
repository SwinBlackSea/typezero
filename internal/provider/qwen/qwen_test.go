package qwen

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
		if got := r.Header.Get("X-DashScope-Wait-Timeout"); got != "30" {
			t.Errorf("X-DashScope-Wait-Timeout = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "qwen3-asr-flash" {
			t.Errorf("model = %v", payload["model"])
		}
		encoded, _ := json.Marshal(payload)
		if !strings.Contains(string(encoded), "data:audio/wav;base64,AQID") {
			t.Errorf("audio data URL missing from %s", encoded)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  你好，世界。  "}}]}`))
	}))
	defer server.Close()

	client := New(server.Client(), server.URL, "secret", "qwen3-asr-flash", 30*time.Second)
	got, err := client.Transcribe(context.Background(), provider.Audio{Data: []byte{1, 2, 3}, MediaType: "audio/wav"})
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}
	if got != "你好，世界。" {
		t.Fatalf("Transcribe() = %q", got)
	}
}

// TestTranscribeRetriesRateLimit verifies that a 429 (DashScope rate limit)
// is retried with backoff and that the eventual success is returned.
func TestTranscribeRetriesRateLimit(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"code":"Throttling.RateQuota"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"重试成功"}}]}`))
	}))
	defer server.Close()

	client := New(server.Client(), server.URL, "secret", "qwen3-asr-flash", 30*time.Second)
	got, err := client.Transcribe(context.Background(), provider.Audio{Data: []byte{1}, MediaType: "audio/wav"})
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}
	if got != "重试成功" {
		t.Fatalf("Transcribe() = %q", got)
	}
	if calls != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls)
	}
}

// TestTranscribeNoRetryOnClientTimeout verifies that a network/context error
// (DashScope queued past the client deadline) is not retried, so a slow
// upstream call cannot be turned into multiple stacked timeouts.
func TestTranscribeNoRetryOnClientTimeout(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	client := New(&http.Client{Timeout: 50 * time.Millisecond}, server.URL, "secret", "qwen3-asr-flash", 30*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := client.Transcribe(ctx, provider.Audio{Data: []byte{1}, MediaType: "audio/wav"})
	if err == nil {
		t.Fatal("Transcribe() error = nil, want timeout")
	}
	if calls != 1 {
		t.Fatalf("expected 1 attempt (no retry on client timeout), got %d", calls)
	}
}
