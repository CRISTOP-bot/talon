package term

import (
	"bufio"
	"os"
	"strings"
	"sync"
)

// History stores previously submitted lines, newest last, and persists them.
type History struct {
	mu      sync.Mutex
	path    string
	entries []string
	max     int
	// inSession dedupes repeated submissions and holds lines not yet saved.
	pending []string
}

// NewHistory loads history from path (missing file is not an error).
func NewHistory(path string, max int) *History {
	if max <= 0 {
		max = 1000
	}
	h := &History{path: path, max: max}
	h.load()
	return h
}

func (h *History) load() {
	if h.path == "" {
		return
	}
	f, err := os.Open(h.path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		h.entries = append(h.entries, line)
	}
	h.trim()
}

func (h *History) trim() {
	if len(h.entries) > h.max {
		h.entries = h.entries[len(h.entries)-h.max:]
	}
}

// Add records a line, skipping blanks and immediate duplicates.
func (h *History) Add(line string) {
	trimmed := strings.TrimRight(line, " \t")
	if strings.TrimSpace(trimmed) == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := len(h.entries); n > 0 && h.entries[n-1] == trimmed {
		return
	}
	h.entries = append(h.entries, trimmed)
	h.pending = append(h.pending, trimmed)
	h.trim()
}

// At returns the i-th most recent entry (0 is the newest).
func (h *History) At(i int) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if i < 0 || i >= len(h.entries) {
		return ""
	}
	return h.entries[len(h.entries)-1-i]
}

// Len returns the number of stored entries.
func (h *History) Len() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.entries)
}

// Search returns entries containing query, newest first.
func (h *History) Search(query string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for i := len(h.entries) - 1; i >= 0; i-- {
		if query == "" || strings.Contains(h.entries[i], query) {
			out = append(out, h.entries[i])
		}
		if len(out) >= 50 {
			break
		}
	}
	return out
}

// Entries returns a copy of the full history, oldest first.
func (h *History) Entries() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.entries...)
}

// Save appends pending entries to the history file. Failures are silent: losing
// history must never interrupt a session.
func (h *History) Save() error {
	h.mu.Lock()
	pending := append([]string(nil), h.pending...)
	h.pending = nil
	path := h.path
	h.mu.Unlock()
	if len(pending) == 0 || path == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	for _, line := range pending {
		if _, err := f.WriteString(strings.ReplaceAll(line, "\n", " ") + "\n"); err != nil {
			return err
		}
	}
	return nil
}
