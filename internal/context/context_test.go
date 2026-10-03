package context

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/talon-cli/talon/internal/config"
	"github.com/talon-cli/talon/internal/index"
	"github.com/talon-cli/talon/internal/memory"
	"github.com/talon-cli/talon/internal/paths"
	"github.com/talon-cli/talon/internal/project"
	"github.com/talon-cli/talon/internal/tools"
)

func fixture(t *testing.T) (Builder, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(paths.EnvDataDir, filepath.Join(home, "data"))
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module x\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("pkg/lib.go", "package pkg\n")
	write("pkg/lib_test.go", "package pkg\n")
	write("README.md", "# x\n")
	write(".talon/memory.md", "Conventions: use tabs.\n")

	ix := index.New(dir)
	if err := ix.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	mem, err := memory.Load(dir, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err := mem.Remember("architecture", "pkg holds the domain logic"); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	cfg.Permissions.Level = config.LevelConfirm

	return Builder{
		Config:    cfg,
		Project:   project.Detect(context.Background(), dir),
		Index:     ix,
		Memory:    mem,
		Workspace: dir,
		GitSummary: "Repository at " + dir + ", branch main. 2 uncommitted change(s):\n" +
			"  modified main.go\n  untracked notes.md\n",
	}, dir
}

func TestSystemPromptIncludesEverythingNeeded(t *testing.T) {
	b, _ := fixture(t)
	prompt := b.System()
	required := []string{
		"You are Talon",              // behavioural contract
		"PROJECT",                    // project block
		"Language: Go",               // detected language
		"go test ./...",              // test command
		"INDEX",                      // index summary
		"Languages:",                 // index breakdown
		"GIT",                        // git state
		"uncommitted change",         // git detail
		"REMEMBERED CONTEXT",         // memory
		"pkg holds the domain logic", // note from the store
		"Conventions: use tabs",      // committed memory file
		"PERMISSIONS",                // permission level
	}
	for _, want := range required {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt missing %q", want)
		}
	}
	if strings.Count(prompt, "\n\n") < 4 {
		t.Errorf("prompt should be structured in blocks:\n%s", prompt)
	}
}

func TestSystemPromptWithoutOptionalInputs(t *testing.T) {
	b := Builder{Config: config.Defaults()}
	prompt := b.System()
	if !strings.Contains(prompt, "You are Talon") {
		t.Errorf("base prompt missing:\n%s", prompt)
	}
	for _, absent := range []string{"PROJECT", "INDEX", "GIT", "REMEMBERED CONTEXT"} {
		if strings.Contains(prompt, absent) {
			t.Errorf("unexpected %q section:\n%s", absent, prompt)
		}
	}
}

func TestExtraAndAppendedText(t *testing.T) {
	b, _ := fixture(t)
	b.Extra = "Extra instructions from the caller."
	if !strings.Contains(b.System(), "Extra instructions from the caller.") {
		t.Error("Extra was not included")
	}
}

func TestWithToolGuide(t *testing.T) {
	got := WithToolGuide("base prompt")
	if !strings.Contains(got, "base prompt") || !strings.Contains(got, "TOOLS") {
		t.Errorf("tool guide = %s", got)
	}
	if !strings.Contains(got, "read_file") || !strings.Contains(got, "git_commit") {
		t.Error("tool guide should name the file and git tools")
	}
}

func TestIndexSummaryOnEmptyIndex(t *testing.T) {
	b := Builder{Index: index.New(t.TempDir())}
	if !strings.Contains(b.indexSummary(), "empty") {
		t.Errorf("summary = %q", b.indexSummary())
	}
}

func TestSuggestRanksTools(t *testing.T) {
	all := tools.All()
	cases := []struct {
		request string
		want    string
	}{
		{"run the tests and fix them", "run_tests"},
		{"find where the parser is defined", "search_text"},
		{"commit my changes", "git_commit"},
		{"create a new module file", "write_file"},
		{"what does this project look like", "inspect_project"},
	}
	for _, c := range cases {
		got := Suggest(c.request, all, 6)
		if len(got) == 0 {
			t.Errorf("no suggestion for %q", c.request)
			continue
		}
		if got[0].Name != c.want {
			names := make([]string, 0, len(got))
			for _, d := range got {
				names = append(names, d.Name)
			}
			t.Errorf("Suggest(%q) = %v, want %s first", c.request, names, c.want)
		}
	}
	if len(Suggest("nothing relevant here", all, 5)) != 0 {
		t.Error("an unrelated request should produce no suggestions")
	}
	if got := Suggest("edit a file and then run the tests", all, 2); len(got) != 2 {
		t.Errorf("limit ignored: %d results", len(got))
	}
}

func TestHotDirsAreListed(t *testing.T) {
	b, _ := fixture(t)
	summary := b.indexSummary()
	if !strings.Contains(summary, "pkg/") {
		t.Errorf("hot directories missing:\n%s", summary)
	}
}
