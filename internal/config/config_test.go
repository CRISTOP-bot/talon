package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CRISTOP-bot/talon/internal/paths"
)

func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(paths.EnvConfigDir, filepath.Join(dir, "config"))
	t.Setenv(paths.EnvDataDir, filepath.Join(dir, "data"))
	t.Setenv(paths.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(paths.EnvCacheDir, filepath.Join(dir, "cache"))
	return dir
}

func TestLoadDefaultsWhenNoFiles(t *testing.T) {
	withTempHome(t)
	proj := t.TempDir()
	c, err := Load(proj)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Permissions.Level != LevelConfirm {
		t.Errorf("default level = %q", c.Permissions.Level)
	}
	if c.Agent.MaxSteps != 24 {
		t.Errorf("default max_steps = %d", c.Agent.MaxSteps)
	}
	if c.Model.Provider != "openai" {
		t.Errorf("default provider = %q", c.Model.Provider)
	}
	if c.UI.Theme != "default" {
		t.Errorf("default theme = %q", c.UI.Theme)
	}
}

func TestProjectOverridesUser(t *testing.T) {
	home := withTempHome(t)
	proj := t.TempDir()
	userDir := filepath.Join(home, "config")
	if err := os.MkdirAll(userDir, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(userDir, "config.toml"), `
model = { provider = "anthropic", name = "user-model", temperature = 0.9 }
[permissions]
level = "read-only"
`)
	write(t, filepath.Join(proj, ProjectDirName, "config.toml"), `
[model]
name = "project-model"
`)
	c, err := Load(proj)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Model.Name != "project-model" {
		t.Errorf("project should win: model.name = %q", c.Model.Name)
	}
	if c.Model.Provider != "anthropic" {
		t.Errorf("user value should survive: provider = %q", c.Model.Provider)
	}
	if c.Model.Temperature != 0.9 {
		t.Errorf("temperature = %v", c.Model.Temperature)
	}
	if c.Permissions.Level != LevelReadOnly {
		t.Errorf("level = %q", c.Permissions.Level)
	}
	if c.UserFile == "" || c.ProjectFile == "" {
		t.Error("file paths should be recorded")
	}
}

func TestEnvOverrides(t *testing.T) {
	withTempHome(t)
	proj := t.TempDir()
	t.Setenv("AI_PROVIDER", "openrouter")
	t.Setenv("AI_MODEL", "anthropic/claude-sonnet-4")
	t.Setenv("AI_API_KEY", "sk-test-1234567890")
	t.Setenv("TALON_PERMISSIONS", "auto")

	c, err := Load(proj)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Model.Provider != "openrouter" {
		t.Errorf("provider = %q", c.Model.Provider)
	}
	if c.Model.Name != "anthropic/claude-sonnet-4" {
		t.Errorf("model = %q", c.Model.Name)
	}
	if c.APIKeyValue() != "sk-test-1234567890" {
		t.Errorf("api key not resolved from env")
	}
	if c.Permissions.Level != LevelFull {
		t.Errorf("TALON_PERMISSIONS=auto should map to full-access, got %q", c.Permissions.Level)
	}
}

func TestAPIKeyEnvOverrideName(t *testing.T) {
	withTempHome(t)
	proj := t.TempDir()
	t.Setenv("MY_TOKEN", "abcd1234")
	c, err := Load(proj)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	c.Model.APIKeyEnv = "MY_TOKEN"
	if got := c.APIKeyValue(); got != "abcd1234" {
		t.Errorf("APIKeyValue = %q", got)
	}
}

func TestSetAndSaveRoundTrip(t *testing.T) {
	withTempHome(t)
	proj := t.TempDir()
	c, err := Load(proj)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := c.Set("model.name", "\"gpt-5-codex\""); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if c.Model.Name != "gpt-5-codex" {
		t.Errorf("after set name = %q", c.Model.Name)
	}
	if err := c.Set("permissions.deny_commands", `["sudo", "rm -rf /"]`); err != nil {
		t.Fatalf("Set list: %v", err)
	}
	if len(c.Permissions.DenyCommands) != 2 {
		t.Errorf("deny list = %v", c.Permissions.DenyCommands)
	}
	if err := c.Set("model.temperature", "0.75"); err != nil {
		t.Fatalf("Set float: %v", err)
	}
	if c.Model.Temperature != 0.75 {
		t.Errorf("temperature = %v", c.Model.Temperature)
	}
	if err := c.SaveUser(); err != nil {
		t.Fatalf("SaveUser: %v", err)
	}
	info, err := os.Stat(c.UserFile)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("config permissions = %v, want 0600", info.Mode().Perm())
	}
	reloaded, err := Load(proj)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.Model.Name != "gpt-5-codex" || reloaded.Model.Temperature != 0.75 {
		t.Errorf("round trip lost values: %+v", reloaded.Model)
	}
}

func TestSetRejectsInvalidValue(t *testing.T) {
	withTempHome(t)
	c, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Set("model.name", "unclosed string"); err == nil {
		t.Error("expected an error for an unquoted multiword value")
	}
}

func TestValidationRejectsBadValues(t *testing.T) {
	withTempHome(t)
	proj := t.TempDir()
	write(t, filepath.Join(proj, ProjectDirName, "config.toml"), `
[permissions]
level = "yolo-ish"
`)
	if _, err := Load(proj); err == nil {
		t.Error("expected an error for an unknown permission level")
	}
}

func TestNormalizeLevelAliases(t *testing.T) {
	cases := map[string]string{
		"interactive": LevelConfirm, "ask": LevelConfirm, "confirm": LevelConfirm,
		"auto": LevelFull, "yolo": LevelFull, "full": LevelFull,
		"RO": LevelReadOnly, "safe": LevelSafe, "nonsense": "nonsense",
	}
	for in, want := range cases {
		if got := normalizeLevel(in); got != want {
			t.Errorf("normalizeLevel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPluginAndMCPParsing(t *testing.T) {
	withTempHome(t)
	proj := t.TempDir()
	write(t, filepath.Join(proj, ProjectDirName, "config.toml"), `
[plugins]
enabled = true

[[plugins.entries]]
name = "docker"
command = "/usr/bin/docker-mcp"
args = ["--stdio"]
enabled = true

[plugins.entries.env]
DOCKER_HOST = "unix:///var/run/docker.sock"

[mcp]
enabled = true

[[mcp.servers]]
name = "filesystem"
command = "mcp-fs"
`)
	c, err := Load(proj)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(c.Plugins.Entries) != 1 {
		t.Fatalf("plugin entries = %+v", c.Plugins.Entries)
	}
	pe := c.Plugins.Entries[0]
	if pe.Name != "docker" || pe.Command != "/usr/bin/docker-mcp" || len(pe.Args) != 1 {
		t.Errorf("plugin = %+v", pe)
	}
	if pe.Env["DOCKER_HOST"] == "" {
		t.Errorf("plugin env = %+v", pe.Env)
	}
	if pe.ToolPrefix != "plugin_docker" {
		t.Errorf("tool prefix = %q", pe.ToolPrefix)
	}
	if !c.MCP.Enabled || len(c.MCP.Servers) != 1 || c.MCP.Servers[0].Command != "mcp-fs" {
		t.Errorf("mcp = %+v", c.MCP)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
