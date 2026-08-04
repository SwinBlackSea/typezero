package hotwords

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hotwords.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp hotwords: %v", err)
	}
	return path
}

func TestLoadSkipsCommentsAndBlanks(t *testing.T) {
	path := writeTemp(t, "# 注释\nGroq\n\nQwen\nDeepSeek\n")
	store := New(path)
	got := store.Terms()
	want := []string{"Groq", "Qwen", "DeepSeek"}
	if len(got) != len(want) {
		t.Fatalf("Terms() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Terms() = %#v, want %#v", got, want)
		}
	}
}

func TestReloadOnFileChange(t *testing.T) {
	path := writeTemp(t, "Groq\n")
	store := New(path)
	if got := store.Terms(); len(got) != 1 || got[0] != "Groq" {
		t.Fatalf("initial Terms() = %#v", got)
	}

	// Adding a hotword must be picked up without recreating the store.
	if err := os.WriteFile(path, []byte("Groq\nTypeZero\n悬浮胶囊\n"), 0o644); err != nil {
		t.Fatalf("rewrite hotwords: %v", err)
	}
	got := store.Terms()
	if len(got) != 3 || got[1] != "TypeZero" || got[2] != "悬浮胶囊" {
		t.Fatalf("after change Terms() = %#v", got)
	}
}

func TestMissingFileStartsEmptyAndRecovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-there.txt")
	store := New(path)
	if got := store.Terms(); len(got) != 0 {
		t.Fatalf("missing file Terms() = %#v", got)
	}
	if err := os.WriteFile(path, []byte("Groq\n"), 0o644); err != nil {
		t.Fatalf("create hotwords: %v", err)
	}
	if got := store.Terms(); len(got) != 1 || got[0] != "Groq" {
		t.Fatalf("after create Terms() = %#v", got)
	}
}

func TestBuildPromptCapsLength(t *testing.T) {
	terms := []string{"Groq", "Qwen", "DeepSeek", "TypeZero", "悬浮胶囊"}
	got := BuildPrompt(terms, 12)
	if strings.Contains(got, "悬浮胶囊") {
		t.Fatalf("BuildPrompt exceeded rune cap: %q", got)
	}
	if got == "" {
		t.Fatal("BuildPrompt returned empty for non-empty terms")
	}
	if BuildPrompt(nil, 100) != "" {
		t.Fatal("BuildPrompt(nil) should be empty")
	}
}
