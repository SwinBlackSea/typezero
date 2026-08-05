package serverconfig

import (
	"path/filepath"
	"testing"
)

func TestNewFallsBackToDefaults(t *testing.T) {
	store := New(filepath.Join(t.TempDir(), "missing.json"), Config{SpeechProvider: "qwen", ChunkSeconds: 30})
	got := store.Get()
	if got.SpeechProvider != "qwen" || got.ChunkSeconds != 30 {
		t.Fatalf("config = %#v", got)
	}
}

func TestUpdatePersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store := New(path, Config{SpeechProvider: "qwen", ChunkSeconds: 30})

	chunk := 10
	got, err := store.Update(nil, &chunk)
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if got.ChunkSeconds != 10 {
		t.Fatalf("chunk = %d", got.ChunkSeconds)
	}

	reloaded := New(path, Config{SpeechProvider: "qwen", ChunkSeconds: 30})
	if reloaded.Get().ChunkSeconds != 10 {
		t.Fatalf("reloaded chunk = %d", reloaded.Get().ChunkSeconds)
	}
}

func TestUpdateRejectsInvalidValues(t *testing.T) {
	store := New("", Config{SpeechProvider: "qwen", ChunkSeconds: 30})

	bad := "whisper"
	if _, err := store.Update(&bad, nil); err == nil {
		t.Fatal("Update() error = nil for bad provider")
	}
	negative := -1
	if _, err := store.Update(nil, &negative); err == nil {
		t.Fatal("Update() error = nil for negative chunk")
	}
	if store.Get().SpeechProvider != "qwen" || store.Get().ChunkSeconds != 30 {
		t.Fatalf("config changed after rejected update: %#v", store.Get())
	}
}
