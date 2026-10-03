package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CRISTOP-bot/talon/internal/llm"
)

func TestSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 10)
	sess := FromMessages("abc", "fix tests", "/tmp/proj", "openai/gpt-5",
		[]llm.Message{
			{Role: llm.RoleUser, Content: "fix the tests"},
			{Role: llm.RoleAssistant, Content: "on it", ToolCalls: []llm.ToolCall{{ID: "1", Name: "run_tests"}}},
			{Role: llm.RoleTool, Name: "run_tests", ToolCallID: "1", Content: "ok"},
		})
	sess.Changes = []string{"a.go"}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "abc.json")); err != nil {
		t.Fatalf("session file missing: %v", err)
	}

	loaded, err := store.Load("abc")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "fix tests" || loaded.Project != "/tmp/proj" {
		t.Errorf("session = %+v", loaded)
	}
	if loaded.MessageCount() != 2 {
		t.Errorf("message count = %d", loaded.MessageCount())
	}
	if len(loaded.Entries) != 3 {
		t.Errorf("entries = %d", len(loaded.Entries))
	}
	if len(loaded.Changes) != 1 {
		t.Errorf("changes = %v", loaded.Changes)
	}
	msgs := loaded.Messages()
	if len(msgs) != 3 || msgs[2].Role != llm.RoleTool || msgs[2].ToolCallID != "1" {
		t.Errorf("messages = %+v", msgs)
	}

	if err := store.Delete("abc"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("abc"); err == nil {
		t.Error("deleted session should not load")
	}
	if err := store.Delete("abc"); err == nil {
		t.Error("deleting twice should error")
	}
}

func TestListFiltersByProjectAndSorts(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 10)
	for i, project := range []string{"/a", "/b", "/a"} {
		sess := &Session{ID: string(rune('a' + i)), Name: "s", Project: project}
		if err := store.Save(sess); err != nil {
			t.Fatal(err)
		}
	}
	all, err := store.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("list = %d, want 3", len(all))
	}
	scoped, err := store.List("/a")
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 2 {
		t.Errorf("scoped list = %d, want 2", len(scoped))
	}
	if scoped[0].Updated.Before(scoped[1].Updated) {
		t.Error("list should be newest first")
	}
}

func TestListOnMissingDirectory(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "nope"), 5)
	list, err := store.List("")
	if err != nil {
		t.Fatalf("a missing directory should not error: %v", err)
	}
	if list != nil {
		t.Errorf("list = %v", list)
	}
}

func TestPruneKeepsTheLimit(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 2)
	for i := 0; i < 5; i++ {
		sess := &Session{ID: "id" + string(rune('a'+i)), Project: "/p"}
		if err := store.Save(sess); err != nil {
			t.Fatal(err)
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) > 2 {
		t.Errorf("%d files kept, want at most 2", len(files))
	}
}

func TestCorruptFileIsReported(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bad.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(dir, 5).Load("bad"); err == nil {
		t.Error("expected a parse error")
	}
	// A corrupt file must not break listing.
	list, err := NewStore(dir, 5).List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("list = %v", list)
	}
}

func TestSanitizeID(t *testing.T) {
	cases := map[string]string{
		"my session":   "my-session",
		"weird/../etc": "weird-..-etc",
		"ok_name-1":    "ok_name-1",
		"   ":          "",
	}
	for in, want := range cases {
		if got := SanitizeID(in); got != want {
			t.Errorf("SanitizeID(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("a", 100)
	if got := SanitizeID(long); len(got) != 64 {
		t.Errorf("long id was not truncated: %d", len(got))
	}
}

func TestNewIDIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		id := NewID()
		if seen[id] {
			t.Fatalf("duplicate id %s", id)
		}
		seen[id] = true
	}
	for id := range seen {
		if _, err := time.Parse("20060102-150405", id[:15]); err != nil {
			t.Errorf("id %q does not start with a timestamp: %v", id, err)
			break
		}
	}
}

func TestFromMessagesDerivesName(t *testing.T) {
	sess := FromMessages("", "", "/p", "m", []llm.Message{
		{Role: llm.RoleUser, Content: "Refactor the parser to use a state machine"},
	})
	if sess.ID == "" {
		t.Error("an id should have been generated")
	}
	if !strings.Contains(sess.Name, "Refactor the parser") {
		t.Errorf("name = %q", sess.Name)
	}
	if sess.FirstRequest() != "Refactor the parser to use a state machine" {
		t.Errorf("first request = %q", sess.FirstRequest())
	}
}

func TestSaveUsesPrivatePermissions(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir, 5)
	if err := store.Save(&Session{ID: "x", Project: "/p"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "x.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("permissions = %v, want 0600", info.Mode().Perm())
	}
}
