package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOverridesAreHonoured(t *testing.T) {
	base := t.TempDir()
	t.Setenv(EnvConfigDir, filepath.Join(base, "c"))
	t.Setenv(EnvDataDir, filepath.Join(base, "d"))
	t.Setenv(EnvStateDir, filepath.Join(base, "s"))
	t.Setenv(EnvCacheDir, filepath.Join(base, "ca"))

	cases := map[string]string{
		Config():      filepath.Join(base, "c"),
		Data():        filepath.Join(base, "d"),
		State():       filepath.Join(base, "s"),
		Cache():       filepath.Join(base, "ca"),
		ConfigFile():  filepath.Join(base, "c", "config.toml"),
		SessionsDir(): filepath.Join(base, "d", "sessions"),
		MemoryDir():   filepath.Join(base, "d", "memory"),
		PluginDir():   filepath.Join(base, "d", "plugins"),
		LogFile():     filepath.Join(base, "s", "logs", "talon.log"),
		HistoryFile(): filepath.Join(base, "s", "history"),
		JournalDir():  filepath.Join(base, "ca", "journal"),
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}

func TestXDGVariablesAreHonoured(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvConfigDir, "")
	t.Setenv(EnvDataDir, "")
	t.Setenv(EnvStateDir, "")
	t.Setenv(EnvCacheDir, "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))

	if got := Config(); got != filepath.Join(home, "config", "talon") {
		t.Errorf("Config = %q", got)
	}
	if got := Data(); got != filepath.Join(home, "data", "talon") {
		t.Errorf("Data = %q", got)
	}
	if got := State(); got != filepath.Join(home, "state", "talon") {
		t.Errorf("State = %q", got)
	}
	if got := Cache(); got != filepath.Join(home, "cache", "talon") {
		t.Errorf("Cache = %q", got)
	}
}

func TestHomeDefaults(t *testing.T) {
	t.Setenv(EnvConfigDir, "")
	t.Setenv(EnvDataDir, "")
	t.Setenv(EnvStateDir, "")
	t.Setenv(EnvCacheDir, "")
	t.Setenv(EnvHome, "/home/tester")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")

	if got := Config(); !strings.HasPrefix(got, "/home/tester") {
		t.Errorf("Config = %q", got)
	}
	if got := LogDir(); !strings.Contains(got, ".local/state") {
		t.Errorf("LogDir = %q", got)
	}
}

func TestEnsureDirAndBinName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "b")
	if err := EnsureDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Errorf("directory not created: %v", err)
	}
	// Creating an existing directory is fine.
	if err := EnsureDir(dir); err != nil {
		t.Errorf("EnsureDir is not idempotent: %v", err)
	}
	if name := BinName("talon"); name != "talon" && name != "talon.exe" {
		t.Errorf("BinName = %q", name)
	}
}
