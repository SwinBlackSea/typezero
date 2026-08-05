// Package serverconfig stores the server-global settings that the client
// edits through the /config endpoints. Values persist to a JSON file so a
// restart keeps them; the file takes precedence over the env-var defaults
// (SPEECH_PROVIDER / CHUNK_SECONDS) once it exists.
package serverconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// Config is the effective server-global configuration.
type Config struct {
	SpeechProvider string `json:"speech_provider"`
	ChunkSeconds   int    `json:"chunk_seconds"`
}

// Store keeps the runtime config and persists it to a JSON file.
type Store struct {
	mu   sync.Mutex
	path string
	cfg  Config
}

// New loads the persisted config from path, falling back to defaults when
// the file is missing, unreadable or invalid.
func New(path string, defaults Config) *Store {
	s := &Store{path: path, cfg: defaults}
	if data, err := os.ReadFile(path); err == nil {
		var loaded Config
		if json.Unmarshal(data, &loaded) == nil && Validate(loaded) == nil {
			s.cfg = loaded
		}
	}
	return s
}

// Get returns a copy of the current config.
func (s *Store) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// Update applies the provided fields (nil = unchanged), validates, persists
// to disk, and only then switches the in-memory config. A persist failure
// leaves the previous config in effect and returns an error.
func (s *Store) Update(speechProvider *string, chunkSeconds *int) (Config, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.cfg
	if speechProvider != nil {
		next.SpeechProvider = *speechProvider
	}
	if chunkSeconds != nil {
		next.ChunkSeconds = *chunkSeconds
	}
	if err := Validate(next); err != nil {
		return Config{}, err
	}
	if s.path != "" {
		data, err := json.MarshalIndent(next, "", "  ")
		if err != nil {
			return Config{}, fmt.Errorf("encode config: %w", err)
		}
		tmp := s.path + ".tmp"
		if err := os.WriteFile(tmp, data, 0o644); err != nil {
			return Config{}, fmt.Errorf("persist config: %w", err)
		}
		if err := os.Rename(tmp, s.path); err != nil {
			return Config{}, fmt.Errorf("persist config: %w", err)
		}
	}
	s.cfg = next
	return next, nil
}

// Validate checks provider names and the chunking interval range.
func Validate(c Config) error {
	if c.SpeechProvider != "qwen" && c.SpeechProvider != "groq" {
		return errors.New("speech_provider 仅支持 qwen 或 groq")
	}
	if c.ChunkSeconds < 0 || c.ChunkSeconds > 120 {
		return errors.New("chunk_seconds 必须在 0 到 120 之间")
	}
	return nil
}
