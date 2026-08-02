package qwen

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"typezero/internal/provider"
)

func TestTranscribe(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "qwen3-asr-flash" {
			t.Errorf("model = %v", payload["model"])
		}
		encoded, _ := json.Marshal(payload)
		if !strings.Contains(string(encoded), "data:audio/mp4;base64,AQID") {
			t.Errorf("audio data URL missing from %s", encoded)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"  你好，世界。  "}}]}`))
	}))
	defer server.Close()

	client := New(server.Client(), server.URL, "secret", "qwen3-asr-flash")
	got, err := client.Transcribe(context.Background(), provider.Audio{Data: []byte{1, 2, 3}, MediaType: "audio/mp4"})
	if err != nil {
		t.Fatalf("Transcribe() error = %v", err)
	}
	if got != "你好，世界。" {
		t.Fatalf("Transcribe() = %q", got)
	}
}
