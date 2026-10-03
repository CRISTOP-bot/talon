// Package session persists conversations so work can be resumed later.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/talon-cli/talon/internal/errs"
	"github.com/talon-cli/talon/internal/llm"
	"github.com/talon-cli/talon/internal/paths"
)

// Entry is one recorded message.
type Entry struct {
	Role string `json:"role"`
	// Content is the message text (assistant replies, user requests).
	Content string `json:"content,omitempty"`
	// Name and ToolCallID link tool results.
	Name       string `json:"name,omitempty"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolCalls are the calls the model requested.
	ToolCalls []llm.ToolCall `json:"tool_calls,omitempty"`
	// Reasoning is kept for the record but never shown again by default.
	Reasoning string `json:"reasoning,omitempty"`
	// Time is when the message was added.
	Time time.Time `json:"time"`
}

// Session is a saved conversation.
type Session struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Project is the workspace the session belongs to.
	Project string `json:"project"`
	Model   string `json:"model"`
	// Summary is a one-line description of what the session was about.
	Summary string    `json:"summary"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	// Entries holds the conversation in order.
	Entries []Entry `json:"entries"`
	// Changes lists the files the session modified (for review).
	Changes []string `json:"changes,omitempty"`
	// Tokens counts the tokens consumed in the session.
	Tokens int `json:"tokens"`
}

// MessageCount returns the number of user/assistant turns.
func (s *Session) MessageCount() int {
	n := 0
	for _, e := range s.Entries {
		if e.Role == string(llm.RoleUser) || e.Role == string(llm.RoleAssistant) {
			n++
		}
	}
	return n
}

// FirstRequest returns the first user message, used as a fallback name.
func (s *Session) FirstRequest() string {
	for _, e := range s.Entries {
		if e.Role == string(llm.RoleUser) {
			return e.Content
		}
	}
	return ""
}

// Store persists sessions as JSON files under the data directory.
type Store struct {
	dir string
	max int

	mu sync.RWMutex
}

// NewStore opens the session store, creating the directory if needed.
func NewStore(dir string, max int) *Store {
	if dir == "" {
		dir = paths.SessionsDir()
	}
	if max <= 0 {
		max = 200
	}
	return &Store{dir: dir, max: max}
}

// Dir returns the directory sessions are stored in.
func (s *Store) Dir() string { return s.dir }

var idPattern = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// SanitizeID makes an arbitrary label usable as a file name.
func SanitizeID(label string) string {
	cleaned := idPattern.ReplaceAllString(strings.TrimSpace(label), "-")
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return ""
	}
	if len(cleaned) > 64 {
		cleaned = cleaned[:64]
	}
	return cleaned
}

// Save writes a session, overwriting an existing file with the same ID.
func (s *Store) Save(sess *Session) error {
	if sess.ID == "" {
		sess.ID = NewID()
	}
	sess.Updated = time.Now()
	if sess.Created.IsZero() {
		sess.Created = sess.Updated
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return errs.Wrap(errs.KindPermission, "session", err)
	}
	data, err := json.MarshalIndent(sess, "", "  ")
	if err != nil {
		return errs.Internal("session", "cannot encode the session: %v", err)
	}
	path := filepath.Join(s.dir, sess.ID+".json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return errs.Wrap(errs.KindPermission, "session", err)
	}
	s.prune()
	return nil
}

// prune keeps at most max sessions per project, deleting the oldest.
func (s *Store) prune() {
	all, err := s.List("")
	if err != nil {
		return
	}
	byProject := map[string][]Session{}
	for _, sess := range all {
		byProject[sess.Project] = append(byProject[sess.Project], sess)
	}
	for _, group := range byProject {
		if len(group) <= s.max {
			continue
		}
		for _, sess := range group[len(group)-s.max:] {
			_ = os.Remove(filepath.Join(s.dir, sess.ID+".json"))
		}
	}
}

// Load reads a session by ID.
func (s *Store) Load(id string) (*Session, error) {
	path := filepath.Join(s.dir, SanitizeID(id)+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, errs.NotFound("session", "no session named %q", id)
	}
	var sess Session
	if err := json.Unmarshal(data, &sess); err != nil {
		return nil, errs.Parse("session", "%s is corrupt: %v", path, err)
	}
	return &sess, nil
}

// Delete removes a session.
func (s *Store) Delete(id string) error {
	path := filepath.Join(s.dir, SanitizeID(id)+".json")
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return errs.NotFound("session", "no session named %q", id)
		}
		return errs.Wrap(errs.KindPermission, "session", err)
	}
	return nil
}

// List returns every session, newest first. When project is non-empty the list
// is restricted to that project.
func (s *Store) List(project string) ([]Session, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errs.Wrap(errs.KindPermission, "session", err)
	}
	var out []Session
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if rerr != nil {
			continue
		}
		var sess Session
		if err := json.Unmarshal(data, &sess); err != nil {
			continue
		}
		if project != "" && sess.Project != project {
			continue
		}
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

// Messages converts a session back into model messages, dropping anything the
// model does not need on resume.
func (s *Session) Messages() []llm.Message {
	out := make([]llm.Message, 0, len(s.Entries))
	for _, e := range s.Entries {
		switch llm.Role(e.Role) {
		case llm.RoleSystem:
			continue
		case llm.RoleUser, llm.RoleAssistant, llm.RoleTool:
			out = append(out, llm.Message{
				Role:       llm.Role(e.Role),
				Content:    e.Content,
				Name:       e.Name,
				ToolCallID: e.ToolCallID,
				ToolCalls:  e.ToolCalls,
			})
		}
	}
	return out
}

// FromMessages builds a session from a conversation.
func FromMessages(id, name, project, model string, msgs []llm.Message) *Session {
	if id == "" {
		id = NewID()
	}
	sess := &Session{ID: id, Name: name, Project: project, Model: model, Created: time.Now()}
	for _, m := range msgs {
		sess.Entries = append(sess.Entries, Entry{
			Role:       string(m.Role),
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
			ToolCalls:  m.ToolCalls,
			Reasoning:  m.Reasoning,
			Time:       time.Now(),
		})
	}
	if name == "" {
		sess.Name = titleFrom(sess.FirstRequest())
	}
	sess.Summary = titleFrom(sess.FirstRequest())
	return sess
}

// NewID returns a sortable identifier.
func NewID() string {
	return fmt.Sprintf("%s-%s", time.Now().Format("20060102-150405"), randomSuffix())
}

func randomSuffix() string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 4)
	f, err := os.Open("/dev/urandom")
	if err == nil {
		defer f.Close()
		raw := make([]byte, 4)
		if _, err := f.Read(raw); err == nil {
			for i, v := range raw {
				b[i] = letters[int(v)%len(letters)]
			}
			return string(b)
		}
	}
	n := time.Now().UnixNano()
	for i := range b {
		b[i] = letters[int(n>>(i*5))%len(letters)]
		n /= 31
	}
	return string(b)
}

func titleFrom(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 60 {
		s = s[:60] + "…"
	}
	return s
}
