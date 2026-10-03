// Package journal records file mutations so they can be undone with /undo and
// reviewed with /diff.
package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one recorded change to a file.
type Entry struct {
	// Seq is the monotonically increasing entry number, starting at 1.
	Seq int `json:"seq"`
	// Path is workspace-relative.
	Path string `json:"path"`
	// Before is the content before the change ("" when the file was created).
	Before string `json:"before"`
	// BeforeExisted distinguishes "was empty" from "did not exist".
	BeforeExisted bool `json:"before_existed"`
	// After is the content after the change.
	After string `json:"after"`
	// Tool is the tool that made the change.
	Tool string `json:"tool"`
	// Time is when the change happened.
	Time time.Time `json:"time"`
}

// Description renders a human summary of the entry.
func (e Entry) Description() string {
	switch {
	case !e.BeforeExisted:
		return fmt.Sprintf("create %s", e.Path)
	case e.After == "":
		return fmt.Sprintf("delete %s", e.Path)
	default:
		return fmt.Sprintf("modify %s", e.Path)
	}
}

// Journal is an ordered list of changes with undo/redo support.
type Journal struct {
	mu      sync.Mutex
	entries []Entry
	cursor  int // number of applied entries
	seq     int
	path    string
}

// New creates an empty journal. When persistPath is non-empty the journal is
// also written to that file as JSON Lines after every change.
func New(persistPath string) *Journal {
	j := &Journal{path: persistPath}
	if persistPath != "" {
		j.load()
	}
	return j
}

func (j *Journal) load() {
	data, err := os.ReadFile(j.path)
	if err != nil {
		return
	}
	for _, line := range splitLines(string(data)) {
		var e Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		j.entries = append(j.entries, e)
		if e.Seq > j.seq {
			j.seq = e.Seq
		}
	}
	j.cursor = len(j.entries)
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			if i > start {
				out = append(out, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// Record appends a change and returns its sequence number.
func (j *Journal) Record(e Entry) int {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.seq++
	e.Seq = j.seq
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	// A new change invalidates anything that was undone.
	j.entries = j.entries[:j.cursor]
	j.entries = append(j.entries, e)
	j.cursor = len(j.entries)
	j.persist(e)
	return e.Seq
}

func (j *Journal) persist(e Entry) {
	if j.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	encoded, err := json.Marshal(e)
	if err != nil {
		return
	}
	_, _ = f.Write(append(encoded, '\n'))
}

// Entries returns the applied entries in order.
func (j *Journal) Entries() []Entry {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]Entry(nil), j.entries[:j.cursor]...)
}

// UndoCount returns how many changes can be reverted.
func (j *Journal) UndoCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cursor
}

// RedoCount returns how many changes can be reapplied.
func (j *Journal) RedoCount() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.entries) - j.cursor
}

// Peek returns the entry that Undo would revert.
func (j *Journal) Peek() (Entry, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cursor == 0 {
		return Entry{}, false
	}
	return j.entries[j.cursor-1], true
}

// Undo marks the last change as reverted (the caller restores the content).
func (j *Journal) Undo() (Entry, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cursor == 0 {
		return Entry{}, false
	}
	j.cursor--
	return j.entries[j.cursor], true
}

// Redo marks the next reverted change as applied again.
func (j *Journal) Redo() (Entry, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.cursor >= len(j.entries) {
		return Entry{}, false
	}
	e := j.entries[j.cursor]
	j.cursor++
	return e, true
}

// Clear forgets every entry.
func (j *Journal) Clear() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.entries = nil
	j.cursor = 0
}

// Summary renders a short list of recent changes.
func (j *Journal) Summary(limit int) []string {
	entries := j.Entries()
	if limit > 0 && len(entries) > limit {
		entries = entries[len(entries)-limit:]
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Description())
	}
	return out
}

// Len returns the number of applied entries.
func (j *Journal) Len() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.cursor
}
