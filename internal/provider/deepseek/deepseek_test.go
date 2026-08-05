package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
		if payload.Messages[0].Content != systemPrompt() {
			t.Errorf("unexpected system prompt = %q", payload.Messages[0].Content)
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

func TestSystemPromptStructuresExplicitListsWithoutForcingThem(t *testing.T) {
	for _, phrase := range []string{
		"1. 2. 3.",
		"普通聊天、单一陈述和自然段不要强行改成列表",
		"不添加事实、观点、标题、事项或解释",
	} {
		if !strings.Contains(systemPrompt(), phrase) {
			t.Errorf("system prompt missing %q", phrase)
		}
	}
}
