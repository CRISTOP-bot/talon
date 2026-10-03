package repl

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CRISTOP-bot/talon/internal/agent"
	"github.com/CRISTOP-bot/talon/internal/config"
	"github.com/CRISTOP-bot/talon/internal/llm"
	"github.com/CRISTOP-bot/talon/internal/paths"
	"github.com/CRISTOP-bot/talon/internal/perm"
	"github.com/CRISTOP-bot/talon/internal/ui"
)

// newTestREPL builds a REPL over a temporary project with a scripted provider.
func newTestREPL(t *testing.T, script []llm.MockTurn) (*REPL, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv(paths.EnvConfigDir, filepath.Join(home, "config"))
	t.Setenv(paths.EnvDataDir, filepath.Join(home, "data"))
	t.Setenv(paths.EnvStateDir, filepath.Join(home, "state"))
	t.Setenv(paths.EnvCacheDir, filepath.Join(home, "cache"))

	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module demo\n\ngo 1.22\n")
	write("main.go", "package main\n\nfunc main() {}\n")
	write("README.md", "# demo\n")

	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.UI.Spinner = false
	cfg.UI.Theme = "mono"
	cfg.Permissions.Level = "full-access"

	buf := &bytes.Buffer{}
	provider := llm.NewMock(llm.Options{})
	provider.Script = script

	r, err := New(Options{
		Config:         cfg,
		Workspace:      dir,
		Stdout:         buf,
		Stderr:         buf,
		Stdin:          os.Stdin,
		Provider:       provider,
		Width:          80,
		NonInteractive: true,
		AutoApprove:    true,
	})
	if err != nil {
		t.Fatalf("repl.New: %v", err)
	}
	return r, buf, dir
}

// slash feeds a line to handleSlash as if the user typed it.
func slash(t *testing.T, r *REPL, line string) string {
	t.Helper()
	before := r.pr.Out.(*bytes.Buffer).Len()
	if !r.handleSlash(context.Background(), line) {
		t.Fatalf("%q was not handled as a command", line)
	}
	out := r.pr.Out.(*bytes.Buffer)
	return out.String()[before:]
}

func TestSlashHelpListsCommands(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/help")
	for _, want := range []string{"/plan", "/undo", "/permissions", "/tools", "/session", "/memory", "/index"} {
		if !strings.Contains(out, want) {
			t.Errorf("/help missing %q:\n%s", want, out)
		}
	}
}

func TestSlashUnknownCommand(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/nonsense")
	if !strings.Contains(out, "unknown command") {
		t.Errorf("output = %s", out)
	}
}

func TestSlashPermissions(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/permissions")
	if !strings.Contains(out, "level:") {
		t.Errorf("summary = %s", out)
	}
	out = slash(t, r, "/permissions read-only")
	if !strings.Contains(out, "read-only") {
		t.Errorf("change = %s", out)
	}
	if r.Mode() != agent.ModeAsk {
		t.Errorf("mode = %v, want ask in read-only", r.Mode())
	}
	decision, _ := r.toolCtx.Policy.Decision(perm.Request{
		Tool: "write_file", Risk: perm.RiskWrite, Path: filepath.Join(r.opts.Workspace, "a.go"),
	})
	if decision != perm.Deny {
		t.Errorf("read-only mode should deny writes, got %v", decision)
	}
	out = slash(t, r, "/permissions bogus")
	if !strings.Contains(out, "unknown level") {
		t.Errorf("invalid level = %s", out)
	}
}

func TestSlashMode(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/mode auto")
	if !strings.Contains(out, "auto") || r.Mode() != agent.ModeAuto {
		t.Errorf("mode = %v (%s)", r.Mode(), out)
	}
	slash(t, r, "/mode ask")
	if r.Mode() != agent.ModeAsk {
		t.Errorf("mode = %v", r.Mode())
	}
	out = slash(t, r, "/mode nonsense")
	if !strings.Contains(out, "unknown mode") {
		t.Errorf("output = %s", out)
	}
}

func TestSlashTools(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/tools")
	for _, want := range []string{"read_file", "git_commit", "builtin"} {
		if !strings.Contains(out, want) {
			t.Errorf("/tools missing %q:\n%s", want, out)
		}
	}
	out = slash(t, r, "/tools suggest fix the failing tests")
	if !strings.Contains(out, "run_tests") {
		t.Errorf("suggestion = %s", out)
	}
	out = slash(t, r, "/tools suggest")
	if !strings.Contains(out, "usage") {
		t.Errorf("empty suggestion = %s", out)
	}
}

func TestSlashContext(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/context")
	for _, want := range []string{"provider", "model", "messages", "indexed files", "system prompt"} {
		if !strings.Contains(out, want) {
			t.Errorf("/context missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "You are Talon") {
		t.Error("/context should show the system prompt")
	}
}

func TestSlashMemory(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/memory")
	if !strings.Contains(out, "no notes") {
		t.Errorf("empty memory = %s", out)
	}
	out = slash(t, r, "/memory add test-command cargo test")
	if !strings.Contains(out, "remembered") {
		t.Errorf("add = %s", out)
	}
	out = slash(t, r, "/memory")
	if !strings.Contains(out, "test-command") {
		t.Errorf("list = %s", out)
	}
	if r.memory.Len() != 1 {
		t.Errorf("notes = %d", r.memory.Len())
	}
	out = slash(t, r, "/memory forget test-command")
	if !strings.Contains(out, "forgot") {
		t.Errorf("forget = %s", out)
	}
	out = slash(t, r, "/memory nonsense")
	if !strings.Contains(out, "unknown subcommand") {
		t.Errorf("output = %s", out)
	}
}

func TestSlashSessionLifecycle(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/session list")
	if !strings.Contains(out, "no saved sessions") {
		t.Errorf("empty list = %s", out)
	}
	out = slash(t, r, "/session save my-work")
	if !strings.Contains(out, "saved as my-work") {
		t.Errorf("save = %s", out)
	}
	out = slash(t, r, "/session list")
	if !strings.Contains(out, "my-work") {
		t.Errorf("list after save = %s", out)
	}
	slash(t, r, "/session load my-work")
	out = slash(t, r, "/session delete my-work")
	if !strings.Contains(out, "deleted") {
		t.Errorf("delete = %s", out)
	}
	out = slash(t, r, "/session load ghost")
	if !strings.Contains(out, "no session") {
		t.Errorf("loading a missing session = %s", out)
	}
}

func TestSlashDiffUndoRedo(t *testing.T) {
	r, _, dir := newTestREPL(t, []llm.MockTurn{
		{Tools: []llm.MockToolCall{{Name: "write_file", Arguments: map[string]any{
			"path": "notes.md", "content": "# notes\n"}}}},
		{Text: "Created the notes file."},
	})
	if err := r.turn(context.Background(), "create notes.md"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	out := slash(t, r, "/diff")
	if !strings.Contains(out, "notes.md") {
		t.Errorf("/diff = %s", out)
	}
	out = slash(t, r, "/undo")
	if !strings.Contains(out, "reverted") {
		t.Errorf("/undo = %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.md")); !os.IsNotExist(err) {
		t.Error("undo did not remove the file")
	}
	out = slash(t, r, "/redo")
	if !strings.Contains(out, "reapplied") {
		t.Errorf("/redo = %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.md")); err != nil {
		t.Error("redo did not restore the file")
	}
	out = slash(t, r, "/undo")
	slash(t, r, "/undo")
	out = slash(t, r, "/undo")
	if !strings.Contains(out, "nothing to undo") {
		t.Errorf("exhausted undo = %s", out)
	}
}

func TestSlashIndex(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/index")
	if !strings.Contains(out, "indexed") {
		t.Errorf("/index = %s", out)
	}
	if r.index.Len() == 0 {
		t.Error("index is empty")
	}
}

func TestSlashModel(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/model")
	if !strings.Contains(out, "provider") || !strings.Contains(out, "model") {
		t.Errorf("/model = %s", out)
	}
	out = slash(t, r, "/model anthropic claude-sonnet-4-5")
	if !strings.Contains(out, "claude-sonnet-4-5") {
		t.Errorf("/model set = %s", out)
	}
}

func TestSlashGitOutsideRepository(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/git")
	if !strings.Contains(out, "not a git repository") {
		t.Errorf("/git = %s", out)
	}
}

func TestSlashPlanAndRun(t *testing.T) {
	r, _, dir := newTestREPL(t, []llm.MockTurn{
		{Text: "1. Read main.go\n2. Change the greeting"},
		{Text: "step one done"},
		{Text: "step two done"},
	})
	out := slash(t, r, "/plan fix the greeting")
	if !strings.Contains(out, "Plan") || !strings.Contains(out, "Read main.go") {
		t.Errorf("/plan = %s", out)
	}
	if r.lastPlan == nil || len(r.lastPlan.Steps) != 2 {
		t.Fatalf("plan = %+v", r.lastPlan)
	}
	// Plan mode must not have changed anything.
	if _, err := os.Stat(filepath.Join(dir, "main.go")); err != nil {
		t.Error("plan mode broke the project")
	}
	out = slash(t, r, "/plan run")
	if !strings.Contains(out, "step 1/2") || !strings.Contains(out, "step 1 done") {
		t.Errorf("/plan run = %s", out)
	}
}

func TestSlashClear(t *testing.T) {
	r, _, _ := newTestREPL(t, []llm.MockTurn{{Text: "hello"}})
	if err := r.turn(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	if r.Conversation().Len() == 0 {
		t.Fatal("conversation is empty")
	}
	out := slash(t, r, "/clear")
	if !strings.Contains(out, "cleared") {
		t.Errorf("/clear = %s", out)
	}
	if r.Conversation().Len() != 0 {
		t.Error("conversation was not cleared")
	}
}

func TestSlashExit(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	slash(t, r, "/exit")
	if !r.exiting {
		t.Error("/exit did not set the exit flag")
	}
}

func TestSlashCompact(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	out := slash(t, r, "/compact")
	if !strings.Contains(out, "nothing to compact") {
		t.Errorf("/compact on a fresh session = %s", out)
	}
}

func TestCompletionAndSuggestions(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	r.setupReadline()
	cmds := r.complete("/")
	if len(cmds) == 0 {
		t.Fatal("no command completions")
	}
	found := false
	for _, c := range cmds {
		if strings.Contains(c, "/plan") {
			found = true
		}
	}
	if !found {
		t.Errorf("completions = %v", cmds)
	}
	if got := r.complete("/hel"); len(got) == 0 {
		t.Error("prefix completion failed")
	}
	files := r.complete("read main.g")
	if len(files) == 0 || !strings.Contains(files[0], "main.go") {
		t.Errorf("path completion = %v", files)
	}
	if got := r.suggest("/pl"); len(got) == 0 {
		t.Error("suggestion lines missing")
	}
	if got := r.suggest("some long prompt that is not a command"); got != nil {
		t.Errorf("unexpected suggestions: %v", got)
	}
}

func TestTurnStreamsAndSavesTheSession(t *testing.T) {
	r, buf, _ := newTestREPL(t, []llm.MockTurn{{Text: "The answer is **42**."}})
	if err := r.turn(context.Background(), "what is the answer?"); err != nil {
		t.Fatalf("turn: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "The answer is") {
		t.Errorf("no answer streamed:\n%s", out)
	}
	if r.activeSession == nil {
		t.Fatal("no session was saved")
	}
	sessions, err := os.ReadDir(filepath.Join(os.Getenv(paths.EnvDataDir), "sessions"))
	if err != nil || len(sessions) == 0 {
		t.Errorf("session not persisted: %v", err)
	}
}

func TestTurnWithAPromptDoesNotStartTheLoop(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv(paths.EnvConfigDir, filepath.Join(home, "config"))
	t.Setenv(paths.EnvDataDir, filepath.Join(home, "data"))
	t.Setenv(paths.EnvStateDir, filepath.Join(home, "state"))
	t.Setenv(paths.EnvCacheDir, filepath.Join(home, "cache"))
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	provider := llm.NewMock(llm.Options{})
	provider.Script = []llm.MockTurn{{Text: "done"}}
	buf := &bytes.Buffer{}
	r, err := New(Options{
		Config: cfg, Workspace: dir, Stdout: buf, Stderr: buf, Stdin: os.Stdin,
		Provider: provider, InitialPrompt: "just answer", NonInteractive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(buf.String(), "done") {
		t.Errorf("prompt was not executed:\n%s", buf.String())
	}
}

func TestTurnWithoutPromptInNonInteractiveModeFails(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv(paths.EnvConfigDir, filepath.Join(home, "config"))
	t.Setenv(paths.EnvDataDir, filepath.Join(home, "data"))
	t.Setenv(paths.EnvStateDir, filepath.Join(home, "state"))
	t.Setenv(paths.EnvCacheDir, filepath.Join(home, "cache"))
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	buf := &bytes.Buffer{}
	r, err := New(Options{
		Config: cfg, Workspace: dir, Stdout: buf, Stderr: buf, Stdin: os.Stdin,
		NonInteractive: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background()); err == nil {
		t.Error("expected a usage error when no prompt was given")
	}
}

func TestBannerMentionsTheProject(t *testing.T) {
	r, buf, _ := newTestREPL(t, nil)
	r.banner()
	out := buf.String()
	for _, want := range []string{"Talon", "project: Go", "tools:", "permissions:"} {
		if !strings.Contains(out, want) {
			t.Errorf("banner missing %q:\n%s", want, out)
		}
	}
}

func TestApproveInAutoMode(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	decision, err := r.Approve(context.Background(), permRequest(), llm.ToolCall{})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Allow || !decision.Always {
		t.Errorf("auto-approve should allow everything: %+v", decision)
	}
}

func TestThemeIsMonoWhenOutputIsNotATerminal(t *testing.T) {
	r, _, _ := newTestREPL(t, nil)
	if r.theme != ui.MonoTheme() {
		t.Errorf("theme = %q", r.theme.Name)
	}
}

// permRequest builds a sample permission request for approval tests.
func permRequest() perm.Request {
	return perm.Request{
		Tool:        "delete_file",
		Risk:        perm.RiskDanger,
		Path:        "main.go",
		Description: "Delete main.go",
	}
}

func TestTextAfterToolActivityIsSeparated(t *testing.T) {
	r, buf, _ := newTestREPL(t, []llm.MockTurn{
		{Text: "Let me look.", Tools: []llm.MockToolCall{{Name: "read_file", Arguments: map[string]any{"path": "main.go"}}}},
		{Text: "It prints nothing interesting."},
	})
	if err := r.turn(context.Background(), "explain main.go"); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	first := strings.Index(out, "Let me look.")
	second := strings.Index(out, "It prints nothing interesting.")
	if first < 0 || second < 0 {
		t.Fatalf("text missing:\n%s", out)
	}
	between := out[first+len("Let me look.") : second]
	if !strings.Contains(between, "\n\n") && !strings.Contains(between, "\n") {
		t.Errorf("the two sentences run together: %q", between)
	}
	if !strings.Contains(between, "read main.go") {
		t.Errorf("the tool result should be visible between them: %q", between)
	}
}

func TestTurnIsRefusedWhenTheProviderIsNotConfigured(t *testing.T) {
	dir := t.TempDir()
	home := t.TempDir()
	t.Setenv(paths.EnvConfigDir, filepath.Join(home, "config"))
	t.Setenv(paths.EnvDataDir, filepath.Join(home, "data"))
	t.Setenv(paths.EnvStateDir, filepath.Join(home, "state"))
	t.Setenv(paths.EnvCacheDir, filepath.Join(home, "cache"))
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	// No API key is available, so the provider cannot be used.
	t.Setenv("AI_API_KEY", "")
	cfg.Model.Provider = "openai"
	cfg.Model.Name = "gpt-5"

	buf := &bytes.Buffer{}
	r, err := New(Options{
		Config: cfg, Workspace: dir, Stdout: buf, Stderr: buf, Stdin: os.Stdin,
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.Ready() {
		t.Skip("an API key is available in this environment")
	}
	if err := r.turn(context.Background(), "hello"); err != nil {
		t.Errorf("the turn should be refused politely, not error: %v", err)
	}
	if !strings.Contains(buf.String(), "AI_API_KEY") {
		t.Errorf("output = %s", buf.String())
	}
}
