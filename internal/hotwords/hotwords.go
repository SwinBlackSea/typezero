// Package hotwords loads a reloadable hotword table from a plain-text file.
// Each line holds one term; blank lines and lines starting with '#' are
// ignored. The table is re-read lazily on access whenever the file's mtime or
// size changes, so operators can add hotwords by editing the file without
// rebuilding or restarting the service.
package hotwords

import (
	"bufio"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Store caches the parsed hotword list and refreshes it when the underlying
// file changes. All exported methods are safe for concurrent use.
type Store struct {
	path string

	mu      sync.RWMutex
	modTime time.Time
	size    int64
	terms   []string
}

// New creates a Store for path and performs the initial load. A missing or
// unreadable file is not an error: the store simply starts empty and picks the
// file up as soon as it appears (or is fixed).
func New(path string) *Store {
	s := &Store{path: path}
	s.reload()
	return s
}

// Terms returns the current hotword list as a copy, refreshing the cache first
// if the file changed since the last read.
func (s *Store) Terms() []string {
	s.refresh()
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, len(s.terms))
	copy(out, s.terms)
	return out
}

func (s *Store) refresh() {
	info, err := os.Stat(s.path)
	if err != nil {
		// File missing or unreadable: drop the cached table rather than
		// serving a stale list that no longer matches the file on disk.
		s.mu.Lock()
		s.modTime = time.Time{}
		s.size = 0
		s.terms = nil
		s.mu.Unlock()
		return
	}
	s.mu.RLock()
	unchanged := info.ModTime().Equal(s.modTime) && info.Size() == s.size
	s.mu.RUnlock()
	if unchanged {
		return
	}
	s.reload()
}

func (s *Store) reload() {
	info, err := os.Stat(s.path)
	var terms []string
	if err == nil {
		terms = readFile(s.path)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.modTime = info.ModTime()
		s.size = info.Size()
	} else {
		s.modTime = time.Time{}
		s.size = 0
	}
	s.terms = terms
}

func readFile(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var terms []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		terms = append(terms, line)
	}
	return terms
}

// BuildPrompt joins hotword terms into a compact guidance fragment for ASR
// prompt fields (e.g. Groq Whisper's prompt). The result is capped at maxRunes
// so a long hotword table cannot blow past the provider's prompt token limit;
// the fragment stops at the last term that still fits.
func BuildPrompt(terms []string, maxRunes int) string {
	if len(terms) == 0 || maxRunes <= 0 {
		return ""
	}
	var sb strings.Builder
	runeCount := 0
	for _, term := range terms {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		termRunes := utf8.RuneCountInString(term)
		separator := 0
		if sb.Len() > 0 {
			separator = 1 // one space
		}
		if runeCount+separator+termRunes > maxRunes {
			break
		}
		if separator > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteString(term)
		runeCount += separator + termRunes
	}
	return sb.String()
}
