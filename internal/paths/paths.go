// Package paths resolves the XDG-style directories Talon uses.
//
// Every location can be overridden with an environment variable, which is what
// tests and sandboxed runs rely on:
//
//	TALON_CONFIG_DIR   user configuration
//	TALON_DATA_DIR     sessions, memory, plugins
//	TALON_STATE_DIR    logs and the input history
//	TALON_CACHE_DIR    indexes and the undo journal
//	TALON_HOME         replaces the user home directory
package paths

import (
	"os"
	"path/filepath"
	"runtime"
)

// Environment variables that override the default locations.
const (
	EnvConfigDir = "TALON_CONFIG_DIR"
	EnvDataDir   = "TALON_DATA_DIR"
	EnvStateDir  = "TALON_STATE_DIR"
	EnvCacheDir  = "TALON_CACHE_DIR"
	EnvHome      = "TALON_HOME"
)

func home() string {
	if h := os.Getenv(EnvHome); h != "" {
		return h
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// xdg resolves a base directory: the override variable first, then the XDG
// variable, then the default under the home directory.
func xdg(override, xdgVar string, fallback []string, parts ...string) string {
	if v := os.Getenv(override); v != "" {
		return filepath.Join(append([]string{v}, parts...)...)
	}
	base := os.Getenv(xdgVar)
	if base == "" {
		switch xdgVar {
		case "XDG_CONFIG_HOME":
			base = filepath.Join(home(), ".config")
		case "XDG_DATA_HOME":
			base = filepath.Join(home(), ".local", "share")
		case "XDG_STATE_HOME":
			base = filepath.Join(home(), ".local", "state")
		case "XDG_CACHE_HOME":
			base = filepath.Join(home(), ".cache")
		default:
			base = filepath.Join(append([]string{home()}, fallback...)...)
		}
	}
	return filepath.Join(append([]string{base, "talon"}, parts...)...)
}

// Config is the user-level configuration directory.
func Config() string { return xdg(EnvConfigDir, "XDG_CONFIG_HOME", nil) }

// Data holds sessions, memory and installed plugins.
func Data() string { return xdg(EnvDataDir, "XDG_DATA_HOME", nil) }

// State holds logs and the input history.
func State() string { return xdg(EnvStateDir, "XDG_STATE_HOME", nil) }

// Cache holds regenerable data: indexes and the undo journal.
func Cache() string { return xdg(EnvCacheDir, "XDG_CACHE_HOME", nil) }

// ConfigFile is the user-level config.toml path.
func ConfigFile() string { return filepath.Join(Config(), "config.toml") }

// LogDir is where log files are written.
func LogDir() string { return filepath.Join(State(), "logs") }

// LogFile is the current log file path.
func LogFile() string { return filepath.Join(LogDir(), "talon.log") }

// HistoryFile is the readline history path.
func HistoryFile() string { return filepath.Join(State(), "history") }

// IndexCacheDir holds per-project symbol indexes.
func IndexCacheDir() string { return filepath.Join(Cache(), "index") }

// JournalDir holds per-project undo journals.
func JournalDir() string { return filepath.Join(Cache(), "journal") }

// SessionsDir holds persisted session transcripts.
func SessionsDir() string { return filepath.Join(Data(), "sessions") }

// MemoryDir holds per-project memory documents.
func MemoryDir() string { return filepath.Join(Data(), "memory") }

// PluginDir holds installed plugins.
func PluginDir() string { return filepath.Join(Data(), "plugins") }

// HistoryPath returns the input history path (used by the REPL).
func HistoryPath() string { return HistoryFile() }

// EnsureDir creates dir (and parents) if missing.
func EnsureDir(dir string) error {
	return os.MkdirAll(dir, 0o755)
}

// BinName returns the platform-specific name of an executable.
func BinName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}
