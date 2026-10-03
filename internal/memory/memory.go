// Package memory stores what Talon should remember about a project between
// sessions: architecture notes, conventions, commands, known errors and the
// user's preferences.
//
// Notes live in the user data directory, keyed by the project path. Nothing
// sensitive is stored automatically: only what the agent was explicitly asked
// to remember, plus notes written inside the project itself.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/paths"
)

// Note is a single remembered fact.
type Note struct {
	Topic string `json:"topic"`
	Body  string `json:"body"`
	// Tags allow filtering, e.g. "convention", "command", "error".
	Tags []string `json:"tags,omitempty"`
	// UpdatedAt is when the note was written.
	UpdatedAt time.Time `json:"updated_at"`
}

// Store is the per-project memory file.
type Store struct {
	path string
	root string

	mu    sync.RWMutex
	notes []Note
	max   int
}

// ProjectMemoryFiles are read from the repository itself and injected into the
// system prompt (they are meant to be committed).
var ProjectMemoryFiles = []string{
	".talon/memory.md",
	"TALON.md",
	"MEMORY.md",
	"AGENTS.md",
	"CLAUDE.md",
	".cursorrules",
}

// Load opens the memory store for a project root.
func Load(root string, maxNotes int) (*Store, error) {
	if maxNotes <= 0 {
		maxNotes = 200
	}
	dir := filepath.Join(paths.MemoryDir(), projectKey(root))
	s := &Store{
		path: filepath.Join(dir, "memory.json"),
		root: root,
		max:  maxNotes,
	}
	s.load()
	return s, nil
}

// projectKey hashes the project path into a stable directory name.
func projectKey(root string) string {
	sum := sha256.Sum256([]byte(root))
	return hex.EncodeToString(sum[:8])
}

func (s *Store) load() {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var file struct {
		Notes []Note `json:"notes"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return
	}
	s.notes = file.Notes
}

// Save writes the store back to disk.
func (s *Store) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.notes) > s.max {
		s.notes = s.notes[len(s.notes)-s.max:]
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return errs.Wrap(errs.KindPermission, "memory", err)
	}
	body := "{\n  \"notes\": [\n"
	for i, n := range s.notes {
		entry, err := json.MarshalIndent(n, "", "  ")
		if err != nil {
			return errs.Internal("memory", "cannot encode a note: %v", err)
		}
		body += indent(string(entry), "    ")
		if i < len(s.notes)-1 {
			body += ","
		}
		body += "\n"
	}
	body += "  ]\n}\n"
	if err := os.WriteFile(s.path, []byte(body), 0o600); err != nil {
		return errs.Wrap(errs.KindPermission, "memory", err)
	}
	return nil
}

// Notes returns all notes sorted by topic.
func (s *Store) Notes() []Note {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := append([]Note(nil), s.notes...)
	sort.Slice(out, func(i, j int) bool { return out[i].Topic < out[j].Topic })
	return out
}

// Len returns the number of notes.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.notes)
}

// Remember adds or replaces a note by topic.
func (s *Store) Remember(topic, body string, tags ...string) error {
	topic = strings.TrimSpace(topic)
	body = strings.TrimSpace(body)
	if topic == "" {
		return errs.Usage("a note needs a topic")
	}
	if body == "" {
		return errs.Usage("a note needs a body")
	}
	if looksSecret(body) {
		return errs.Permission("memory",
			"refusing to remember %q: it looks like it contains a credential", topic)
	}
	s.mu.Lock()
	for i := range s.notes {
		if strings.EqualFold(s.notes[i].Topic, topic) {
			s.notes[i].Body = body
			s.notes[i].Tags = tags
			s.notes[i].UpdatedAt = time.Now()
			s.mu.Unlock()
			return s.Save()
		}
	}
	s.notes = append(s.notes, Note{Topic: topic, Body: body, Tags: tags, UpdatedAt: time.Now()})
	s.mu.Unlock()
	return s.Save()
}

// Forget removes a note by topic.
func (s *Store) Forget(topic string) error {
	s.mu.Lock()
	before := len(s.notes)
	kept := s.notes[:0]
	for _, n := range s.notes {
		if !strings.EqualFold(n.Topic, topic) {
			kept = append(kept, n)
		}
	}
	s.notes = kept
	s.mu.Unlock()
	if len(s.notes) == before {
		return errs.NotFound("memory", "no note titled %q", topic)
	}
	return s.Save()
}

// Search returns notes whose topic or body matches query.
func (s *Store) Search(query string) []Note {
	q := strings.ToLower(query)
	var out []Note
	for _, n := range s.Notes() {
		if q == "" || strings.Contains(strings.ToLower(n.Topic), q) ||
			strings.Contains(strings.ToLower(n.Body), q) {
			out = append(out, n)
		}
	}
	return out
}

// Clear removes every note.
func (s *Store) Clear() error {
	s.mu.Lock()
	s.notes = nil
	s.mu.Unlock()
	return s.Save()
}

// Render turns the notes into the block injected into the system prompt.
func (s *Store) Render(limit int) string {
	notes := s.Notes()
	if len(notes) == 0 {
		return ""
	}
	if limit > 0 && len(notes) > limit {
		notes = notes[:limit]
	}
	var b strings.Builder
	for _, n := range notes {
		fmt.Fprintf(&b, "- %s: %s\n", n.Topic, oneline(n.Body))
	}
	return b.String()
}

// ReadProjectMemory reads memory files committed inside the project.
func ReadProjectMemory(root string) (string, []string) {
	var parts []string
	var files []string
	for _, rel := range ProjectMemoryFiles {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			continue
		}
		content := strings.TrimSpace(string(data))
		if content == "" {
			continue
		}
		if len(content) > 8000 {
			content = content[:8000] + "\n… (truncated)"
		}
		parts = append(parts, fmt.Sprintf("### %s\n%s", rel, content))
		files = append(files, rel)
	}
	return strings.Join(parts, "\n\n"), files
}

var secretPattern = regexp.MustCompile(`(?i)(api[_-]?key|secret|password|token|bearer\s)[=: ]\s*\S{8,}`)

// looksSecret reports whether text seems to contain a credential.
func looksSecret(text string) bool {
	return secretPattern.MatchString(text)
}

func oneline(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = prefix + lines[i]
	}
	return strings.Join(lines, "\n")
}
