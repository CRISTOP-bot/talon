package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CRISTOP-bot/talon/internal/paths"
)

func tempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(paths.EnvDataDir, filepath.Join(dir, "data"))
	return dir
}

func TestRememberAndReload(t *testing.T) {
	tempHome(t)
	project := "/tmp/project-one"
	store, err := Load(project, 10)
	if err != nil {
		t.Fatal(err)
	}
	if store.Len() != 0 {
		t.Fatalf("a fresh store should be empty, got %d", store.Len())
	}
	if err := store.Remember("architecture", "The API layer lives in internal/http; storage in internal/store.", "layout"); err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("test command", "make test"); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(project, 10)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Len() != 2 {
		t.Fatalf("notes = %d, want 2", reloaded.Len())
	}
	rendered := reloaded.Render(0)
	if !strings.Contains(rendered, "architecture") || !strings.Contains(rendered, "make test") {
		t.Errorf("render = %q", rendered)
	}
	// Notes for another project must not leak in.
	other, err := Load("/tmp/project-two", 10)
	if err != nil {
		t.Fatal(err)
	}
	if other.Len() != 0 {
		t.Errorf("memory leaked between projects: %d", other.Len())
	}
}

func TestRememberReplacesByTopic(t *testing.T) {
	tempHome(t)
	store, err := Load("/tmp/p", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("convention", "tabs"); err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("Convention", "spaces"); err != nil {
		t.Fatal(err)
	}
	if store.Len() != 1 {
		t.Errorf("notes = %d, want 1", store.Len())
	}
	notes := store.Search("spaces")
	if len(notes) != 1 || !strings.EqualFold(notes[0].Topic, "convention") {
		t.Errorf("notes = %+v", notes)
	}
}

func TestForget(t *testing.T) {
	tempHome(t)
	store, err := Load("/tmp/p", 10)
	if err != nil {
		t.Fatal(err)
	}
	_ = store.Remember("a", "b")
	if err := store.Forget("a"); err != nil {
		t.Fatal(err)
	}
	if store.Len() != 0 {
		t.Error("note was not forgotten")
	}
	if err := store.Forget("missing"); err == nil {
		t.Error("expected an error for an unknown topic")
	}
}

func TestRejectsEmptyAndSecrets(t *testing.T) {
	tempHome(t)
	store, err := Load("/tmp/p", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Remember("", "body"); err == nil {
		t.Error("expected an error for an empty topic")
	}
	if err := store.Remember("topic", "  "); err == nil {
		t.Error("expected an error for an empty body")
	}
	if err := store.Remember("credentials", "api_key=sk-1234567890abcdef"); err == nil {
		t.Error("secrets must not be stored")
	}
	if store.Len() != 0 {
		t.Error("nothing should have been stored")
	}
}

func TestSearchFiltersByText(t *testing.T) {
	tempHome(t)
	store, _ := Load("/tmp/p", 10)
	_ = store.Remember("build", "cargo build --release")
	_ = store.Remember("tests", "cargo test")
	if len(store.Search("cargo")) != 2 {
		t.Error("search by body failed")
	}
	if len(store.Search("build")) != 1 {
		t.Error("search by topic failed")
	}
	if len(store.Search("nothing here")) != 0 {
		t.Error("unexpected match")
	}
}

func TestClear(t *testing.T) {
	tempHome(t)
	store, _ := Load("/tmp/p", 10)
	_ = store.Remember("x", "y")
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if store.Len() != 0 {
		t.Error("Clear left notes behind")
	}
}

func TestMaxNotesIsEnforced(t *testing.T) {
	tempHome(t)
	store, _ := Load("/tmp/p", 3)
	for i := 0; i < 6; i++ {
		if err := store.Remember(string(rune('a'+i)), "body"); err != nil {
			t.Fatal(err)
		}
	}
	if store.Len() > 3 {
		t.Errorf("notes = %d, want at most 3", store.Len())
	}
}

func TestReadProjectMemory(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".talon"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".talon", "memory.md"), []byte("Use tabs.\nRun make test.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	content, files := ReadProjectMemory(root)
	if !strings.Contains(content, "Use tabs") || !strings.Contains(content, "make test") {
		t.Errorf("content = %q", content)
	}
	if len(files) != 1 || files[0] != ".talon/memory.md" {
		t.Errorf("files = %v", files)
	}

	empty := t.TempDir()
	if content, _ := ReadProjectMemory(empty); content != "" {
		t.Errorf("expected nothing, got %q", content)
	}
}

func TestRenderTruncates(t *testing.T) {
	tempHome(t)
	store, _ := Load("/tmp/p", 10)
	_ = store.Remember("long", strings.Repeat("x", 1000))
	out := store.Render(0)
	if len([]rune(out)) > 320 {
		t.Errorf("render is not truncated: %d runes", len([]rune(out)))
	}
}

func TestLimitArgument(t *testing.T) {
	tempHome(t)
	store, _ := Load("/tmp/p", 10)
	for i := 0; i < 5; i++ {
		_ = store.Remember(string(rune('a'+i)), "body")
	}
	if lines := strings.Count(store.Render(2), "\n"); lines != 2 {
		t.Errorf("Render(2) produced %d lines", lines)
	}
}
