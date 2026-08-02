package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPolishDisablesThinking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload request
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Model != "deepseek-v4-flash" {
			t.Errorf("model = %q", payload.Model)
		}
		if payload.Thinking.Type != "disabled" {
			t.Errorf("thinking.type = %q", payload.Thinking.Type)
		}
		if len(payload.Messages) != 2 || payload.Messages[1].Content != "我我想测试" {
			t.Errorf("messages = %#v", payload.Messages)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"我想测试。"}}]}`))
	}))
	defer server.Close()

	client := New(server.Client(), server.URL, "secret", "deepseek-v4-flash")
	got, err := client.Polish(context.Background(), "我我想测试")
	if err != nil {
		t.Fatalf("Polish() error = %v", err)
	}
	if got != "我想测试。" {
		t.Fatalf("Polish() = %q", got)
	}
}
