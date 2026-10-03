package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/talon-cli/talon/internal/cli"
	"github.com/talon-cli/talon/internal/paths"
)

// newTestApp wires the real command set against temporary directories. The
// project directory is passed to every command through --cwd, so tests never
// touch the repository they run from.
func newTestApp(t *testing.T, project string) (*cli.App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	home := t.TempDir()
	t.Setenv(paths.EnvConfigDir, filepath.Join(home, "config"))
	t.Setenv(paths.EnvDataDir, filepath.Join(home, "data"))
	t.Setenv(paths.EnvStateDir, filepath.Join(home, "state"))
	t.Setenv(paths.EnvCacheDir, filepath.Join(home, "cache"))

	testProject = project
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a := cli.NewApp()
	a.Stdout = out
	a.Stderr = errOut
	cli.SetSessionRunner(nil)
	Register(a)
	return a, out, errOut
}

// run executes argv inside the test project and returns the combined output.
func run(t *testing.T, a *cli.App, argv ...string) (string, int) {
	t.Helper()
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	a.Stdout = out
	a.Stderr = errOut
	full := append([]string{"talon", "--cwd", testProject}, argv...)
	code := a.Run(full)
	return out.String() + errOut.String(), code
}

// testProject is the project directory the current test runs against.
var testProject string

func newProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module demo\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestVersionCommand(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)
	out, code := run(t, a, "version")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, want := range []string{"talon", "commit:", "go:"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestInitCommandWritesConfig(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)

	out, code := run(t, a, "init", "--provider", "anthropic", "--model", "claude-sonnet-4-5")
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	cfgPath := filepath.Join(os.Getenv(paths.EnvConfigDir), "config.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !strings.Contains(string(data), "anthropic") || !strings.Contains(string(data), "claude-sonnet-4-5") {
		t.Errorf("config = %s", data)
	}
	if !strings.Contains(out, "wrote") {
		t.Errorf("output = %s", out)
	}
	// A second run must refuse without --force.
	if _, code := run(t, a, "init"); code == 0 {
		t.Error("init should refuse to overwrite an existing file")
	}
	if _, code := run(t, a, "init", "--force", "--provider", "openai"); code != 0 {
		t.Errorf("--force failed: %d", code)
	}
}

func TestConfigCommandLifecycle(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)

	out, code := run(t, a, "config", "list")
	if code != 0 || !strings.Contains(out, "model.provider") {
		t.Fatalf("list failed (%d): %s", code, out)
	}
	if strings.Contains(out, "AI_API_KEY_SECRET") {
		t.Error("list must not leak secrets")
	}

	if _, code := run(t, a, "config", "set", "model.name", "\"gpt-5\""); code != 0 {
		t.Fatalf("set failed: %d", code)
	}
	out, _ = run(t, a, "config", "get", "model.name")
	if !strings.Contains(out, "gpt-5") {
		t.Errorf("get = %s", out)
	}
	out, _ = run(t, a, "config", "path")
	if !strings.Contains(out, "user:") || !strings.Contains(out, "project:") {
		t.Errorf("path = %s", out)
	}

	// Invalid values must be rejected.
	if _, code := run(t, a, "config", "set", "model.name", "unclosed"); code == 0 {
		t.Error("an unquoted multiword value should fail")
	}
	if _, code := run(t, a, "config", "get", "nope.key"); code == 0 {
		t.Error("getting an unknown key should fail")
	}
	if _, code := run(t, a, "config", "nonsense"); code == 0 {
		t.Error("an unknown subcommand should fail")
	}
	if _, code := run(t, a, "config", "set", "permissions.level", "\"bogus\""); code == 0 {
		t.Error("an invalid permission level should be rejected")
	}
	if _, code := run(t, a, "config", "unset", "model.name"); code != 0 {
		t.Errorf("unset failed: %d", code)
	}
	if _, code := run(t, a, "config", "unset", "model.name"); code == 0 {
		t.Error("unsetting a missing key should fail")
	}
}

func TestConfigProjectScope(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)
	if _, code := run(t, a, "config", "set", "permissions.level", "\"safe\"", "--project"); code != 0 {
		t.Fatalf("set --project failed: %d", code)
	}
	data, err := os.ReadFile(filepath.Join(project, ".talon", "config.toml"))
	if err != nil {
		t.Fatalf("project config not written: %v", err)
	}
	if !strings.Contains(string(data), "safe") {
		t.Errorf("project config = %s", data)
	}
}

func TestDoctorCommand(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)
	out, code := run(t, a, "doctor")
	if code != 0 && code != 1 && code != 2 {
		t.Fatalf("unexpected exit code %d", code)
	}
	for _, want := range []string{"config", "project root", "terminal", "git"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestModelsCommandOffline(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)
	out, code := run(t, a, "models", "--offline")
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "built-in defaults") {
		t.Errorf("output = %s", out)
	}
	if !strings.Contains(out, "gpt") && !strings.Contains(out, "claude") {
		t.Errorf("no models listed:\n%s", out)
	}
}

func TestModelsCommandSet(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)
	out, code := run(t, a, "models", "--offline", "--set", "gpt-4.1")
	if code != 0 {
		t.Fatalf("exit code = %d\n%s", code, out)
	}
	if !strings.Contains(out, "gpt-4.1") {
		t.Errorf("output = %s", out)
	}
	listed, _ := run(t, a, "config", "get", "model.name")
	if !strings.Contains(listed, "gpt-4.1") {
		t.Errorf("model was not persisted: %s", listed)
	}
}

func TestToolsCommand(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)
	out, code := run(t, a, "tools")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, want := range []string{"read_file", "run_command", "git_commit", "delete_file", "risk", "builtin"} {
		if !strings.Contains(out, want) {
			t.Errorf("tools output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "git_push") {
		t.Error("there must be no push tool")
	}
	detailed, _ := run(t, a, "tools", "--describe")
	if len(detailed) <= len(out) {
		t.Error("--describe should print more than the summary")
	}
}

func TestPermissionsCommand(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)
	out, code := run(t, a, "permissions")
	if code != 0 || !strings.Contains(out, "level:") {
		t.Fatalf("show failed (%d): %s", code, out)
	}
	out, code = run(t, a, "permissions", "safe", "--save")
	if code != 0 || !strings.Contains(out, "saved") {
		t.Fatalf("save failed (%d): %s", code, out)
	}
	listed, _ := run(t, a, "config", "get", "permissions.level")
	if !strings.Contains(listed, "safe") {
		t.Errorf("level not persisted: %s", listed)
	}
	if _, code := run(t, a, "permissions", "nonsense"); code == 0 {
		t.Error("an unknown level should be rejected")
	}
	unsaved, code := run(t, a, "permissions", "read-only")
	if code != 0 || !strings.Contains(unsaved, "not saved") {
		t.Errorf("without --save nothing should be written: %s", unsaved)
	}
}

func TestSessionsCommand(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)
	out, code := run(t, a, "sessions", "list")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	if !strings.Contains(out, "no sessions") {
		t.Errorf("output = %s", out)
	}
	if _, code := run(t, a, "sessions", "show"); code == 0 {
		t.Error("show without an id should fail")
	}
	if _, code := run(t, a, "sessions", "delete", "missing"); code == 0 {
		t.Error("deleting a missing session should fail")
	}
	if _, code := run(t, a, "sessions", "nonsense"); code == 0 {
		t.Error("an unknown subcommand should fail")
	}
}

func TestPluginsCommandList(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)
	out, code := run(t, a, "plugins", "list")
	if code != 0 || !strings.Contains(out, "no plugins") {
		t.Fatalf("list failed (%d): %s", code, out)
	}
	if _, code := run(t, a, "plugins", "install"); code == 0 {
		t.Error("install without a source should fail")
	}
	if _, code := run(t, a, "plugins", "remove", "ghost"); code == 0 {
		t.Error("removing a missing plugin should fail")
	}
	if _, code := run(t, a, "plugins", "nonsense"); code == 0 {
		t.Error("an unknown subcommand should fail")
	}
}

func TestUpdateCommandReportsMissingReleases(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)
	t.Setenv("TALON_UPDATE_API", "http://127.0.0.1:1")
	out, code := run(t, a, "update", "--check")
	if code == 0 {
		t.Fatalf("expected a failure when the release API is unreachable:\n%s", out)
	}
	if !strings.Contains(out, "error") {
		t.Errorf("output = %s", out)
	}
}

func TestHelpListsEveryCommand(t *testing.T) {
	project := newProject(t)
	a, _, _ := newTestApp(t, project)
	out, code := run(t, a, "help")
	if code != 0 {
		t.Fatalf("exit code = %d", code)
	}
	for _, want := range []string{
		"config", "doctor", "init", "models", "permissions",
		"plugins", "sessions", "tools", "update", "version",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("help is missing %q:\n%s", want, out)
		}
	}
}
