// Package integration exercises Talon end to end: a real REPL, real tools, a
// scripted model and a real plugin process. Nothing here touches the network.
package integration

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/talon-cli/talon/internal/config"
	"github.com/talon-cli/talon/internal/llm"
	"github.com/talon-cli/talon/internal/logger"
	"github.com/talon-cli/talon/internal/paths"
	"github.com/talon-cli/talon/internal/repl"
)

// newProject writes a small Go project in a temp dir and returns its path.
func newProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"go.mod":        "module example.com/fixme\n\ngo 1.22\n",
		"main.go":       "package main\n\nfunc main() {\n\tprintln(greeting(\"world\"))\n}\n",
		"greet.go":      "package main\n\nfunc greeting(name string) string {\n\treturn \"hi \" + name\n}\n",
		"greet_test.go": "package main\n\nimport \"testing\"\n\nfunc TestGreeting(t *testing.T) {\n\tif got := greeting(\"world\"); got != \"hello world\" {\n\t\tt.Fatalf(\"got %q\", got)\n\t}\n}\n",
	}
	for rel, body := range files {
		path := filepath.Join(dir, rel)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// newREPL builds a REPL with a scripted provider writing to buf.
func newREPL(t *testing.T, dir string, provider llm.Provider, buf *bytes.Buffer, mutate func(*repl.Options)) *repl.REPL {
	t.Helper()
	home := t.TempDir()
	t.Setenv(paths.EnvConfigDir, filepath.Join(home, "config"))
	t.Setenv(paths.EnvDataDir, filepath.Join(home, "data"))
	t.Setenv(paths.EnvStateDir, filepath.Join(home, "state"))
	t.Setenv(paths.EnvCacheDir, filepath.Join(home, "cache"))

	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Model.Provider = "mock"
	cfg.Model.Name = "mock-coder"
	cfg.UI.Spinner = false
	cfg.Permissions.Level = "full-access"
	cfg.Sessions.Autosave = true

	opts := repl.Options{
		Config:    cfg,
		Workspace: dir,
		Stdout:    buf,
		Stderr:    buf,
		Stdin:     os.Stdin,
		Log:       logger.Discard,
		Provider:  provider,
		Width:     80,
	}
	if mutate != nil {
		mutate(&opts)
	}
	r, err := repl.New(opts)
	if err != nil {
		t.Fatalf("repl.New: %v", err)
	}
	return r
}

// runPrompt drives the non-interactive path for a single prompt.
func runPrompt(t *testing.T, r *repl.REPL) string {
	t.Helper()
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return ""
}

func TestEndToEndFixesATest(t *testing.T) {
	dir := newProject(t)
	var buf bytes.Buffer

	provider := llm.NewMock(llm.Options{})
	provider.Script = []llm.MockTurn{
		{Text: "Let me look at the code and the failing test.", Tools: []llm.MockToolCall{
			{Name: "run_tests", Arguments: map[string]any{}},
		}},
		{Tools: []llm.MockToolCall{
			{Name: "read_file", Arguments: map[string]any{"path": "greet.go"}},
		}},
		{Tools: []llm.MockToolCall{
			{Name: "edit_file", Arguments: map[string]any{
				"path":       "greet.go",
				"old_string": `return "hi " + name`,
				"new_string": `return "hello " + name`,
			}},
		}},
		{Tools: []llm.MockToolCall{
			{Name: "run_tests", Arguments: map[string]any{}},
		}},
		{Text: "Fixed `greeting` so it returns \"hello world\"; the suite passes now."},
	}

	r := newREPL(t, dir, provider, &buf, func(o *repl.Options) {
		o.NonInteractive = true
		o.InitialPrompt = "the greeting test is failing, fix it"
		o.AutoApprove = true
	})
	runPrompt(t, r)

	out := buf.String()
	if !strings.Contains(out, "edited greet.go") {
		t.Errorf("the edit was not applied; transcript:\n%s", out)
	}
	body, err := os.ReadFile(filepath.Join(dir, "greet.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `return "hello " + name`) {
		t.Errorf("greet.go was not modified:\n%s", body)
	}
	if !strings.Contains(out, "suite passes") {
		t.Errorf("final answer missing from transcript:\n%s", out)
	}
	// The session must have been persisted for later resume.
	sessions := filepath.Join(os.Getenv(paths.EnvDataDir), "sessions")
	entries, err := os.ReadDir(sessions)
	if err != nil || len(entries) == 0 {
		t.Errorf("no session was saved (%v)", err)
	}
}

func TestEndToEndPlanModeProducesNoChanges(t *testing.T) {
	dir := newProject(t)
	var buf bytes.Buffer

	provider := llm.NewMock(llm.Options{})
	provider.Script = []llm.MockTurn{
		{Text: "1. Read `greet.go` and see that it returns \"hi \"\n2. Change it to return \"hello \"\n3. Run `go test ./...`"},
	}
	r := newREPL(t, dir, provider, &buf, func(o *repl.Options) {
		o.NonInteractive = true
		o.InitialPrompt = "/plan how do I fix the failing greeting test?"
	})
	runPrompt(t, r)

	out := buf.String()
	if !strings.Contains(out, "Plan") || !strings.Contains(out, "Change it to return") {
		t.Errorf("plan not rendered:\n%s", out)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "greet.go"))
	if !strings.Contains(string(body), `return "hi " + name`) {
		t.Error("plan mode changed a file")
	}
}

func TestEndToEndWithRealPlugin(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain required to build the example plugin")
	}
	dir := newProject(t)
	var buf bytes.Buffer

	// Build the example plugin into a plugin root, the way `plugins install`
	// lays it out: <root>/<name>/talon-plugin.json + the executable.
	pluginRoot := t.TempDir()
	pluginDir := filepath.Join(pluginRoot, "hello")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	source := pluginSource(t)
	build := exec.Command("go", "build", "-o", filepath.Join(pluginDir, "hello"), ".")
	build.Dir = source
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the example plugin: %v\n%s", err, out)
	}
	manifest, err := os.ReadFile(filepath.Join(source, "talon-plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "talon-plugin.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}

	provider := llm.NewMock(llm.Options{})
	provider.Script = []llm.MockTurn{
		{Tools: []llm.MockToolCall{
			{Name: "hello_greet", Arguments: map[string]any{"name": "Ada"}},
		}},
		{Text: "The plugin greeted Ada."},
	}

	r := newREPL(t, dir, provider, &buf, func(o *repl.Options) {
		o.NonInteractive = true
		o.InitialPrompt = "use the plugin to greet Ada"
		o.AutoApprove = true
		o.Config.Plugins.Dir = pluginRoot
		o.Config.Plugins.Enabled = true
	})
	runPrompt(t, r)

	out := buf.String()
	if !strings.Contains(out, "hello_greet") {
		t.Errorf("the plugin tool was not offered to the model:\n%s", out)
	}
	if !strings.Contains(out, "Ada") {
		t.Errorf("the plugin result was not surfaced:\n%s", out)
	}
	if len(r.Plugins()) != 1 {
		t.Errorf("expected one loaded plugin, got %d", len(r.Plugins()))
	}
}

// pluginSource returns the directory of the bundled example plugin.
func pluginSource(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "..", "..", "examples", "plugins", "hello")
}

// pipeStdin returns a readable *os.File fed by s, so the confirmation prompt can
// be answered exactly as a user would.
func pipeStdin(t *testing.T, answer string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer w.Close()
		_, _ = w.WriteString(answer + "\n")
	}()
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func TestConfirmationPromptIsHonoured(t *testing.T) {
	dir := newProject(t)
	var buf bytes.Buffer

	provider := llm.NewMock(llm.Options{})
	provider.Script = []llm.MockTurn{
		{Tools: []llm.MockToolCall{{Name: "delete_file", Arguments: map[string]any{"path": "greet.go"}}}},
		{Text: "I left the file alone."},
	}

	r := newREPL(t, dir, provider, &buf, func(o *repl.Options) {
		o.NonInteractive = true
		o.InitialPrompt = "delete greet.go"
		o.Stdin = pipeStdin(t, "n")
	})
	runPrompt(t, r)

	out := buf.String()
	if !strings.Contains(out, "needs your confirmation") {
		t.Errorf("no confirmation prompt was shown:\n%s", out)
	}
	if !strings.Contains(out, "Delete greet.go") {
		t.Errorf("prompt does not describe the action:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "greet.go")); err != nil {
		t.Error("the file was deleted despite the rejection")
	}
}

func TestConfirmationPromptAccepts(t *testing.T) {
	dir := newProject(t)
	var buf bytes.Buffer

	provider := llm.NewMock(llm.Options{})
	provider.Script = []llm.MockTurn{
		{Tools: []llm.MockToolCall{{Name: "delete_file", Arguments: map[string]any{"path": "greet.go"}}}},
		{Text: "Deleted."},
	}

	r := newREPL(t, dir, provider, &buf, func(o *repl.Options) {
		o.NonInteractive = true
		o.InitialPrompt = "delete greet.go"
		o.Stdin = pipeStdin(t, "y")
	})
	runPrompt(t, r)

	if _, err := os.Stat(filepath.Join(dir, "greet.go")); !os.IsNotExist(err) {
		t.Error("the file was not deleted after approval")
	}
	if !strings.Contains(buf.String(), "✓") {
		t.Errorf("no success indication:\n%s", buf.String())
	}
}
