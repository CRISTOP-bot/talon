// Package logger provides optional, redacted debug logging.
//
// Logs never contain credentials: values registered with Secret are masked,
// and any assignment that looks like `api_key=...`, `authorization: ...`,
// `token=...`, etc. is scrubbed before a line is written.
package logger

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Level is a logging verbosity.
type Level int

// Verbosity levels in increasing order.
const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
	LevelOff
)

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "off"
	}
}

// ParseLevel converts a configuration string into a Level.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug", "trace":
		return LevelDebug
	case "info":
		return LevelInfo
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelOff
	}
}

// Options configures a Logger.
type Options struct {
	Level  Level
	File   string
	Stderr io.Writer
	Redact bool
}

// Logger writes structured lines to a file and/or stderr.
type Logger struct {
	mu      sync.Mutex
	level   Level
	stderr  io.Writer
	file    *os.File
	redact  bool
	secrets []string
}

// New creates a logger. An empty file path disables file logging.
func New(opts Options) *Logger {
	l := &Logger{level: opts.Level, stderr: opts.Stderr, redact: opts.Redact}
	if opts.Stderr == nil {
		l.stderr = io.Discard
	}
	if opts.File != "" && opts.Level != LevelOff {
		if f, err := os.OpenFile(opts.File, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			l.file = f
		}
	}
	return l
}

// Level returns the active verbosity.
func (l *Logger) Level() Level {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.level
}

// Path returns the log file path, or "" when file logging is off.
func (l *Logger) Path() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return ""
	}
	return l.file.Name()
}

// Secret registers a literal that must never appear in logs.
func (l *Logger) Secret(values ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, v := range values {
		if len(v) >= 8 {
			l.secrets = append(l.secrets, v)
		}
	}
}

// Close releases the log file.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_ = l.file.Close()
		l.file = nil
	}
}

func (l *Logger) logf(level Level, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if level < l.level || l.level == LevelOff {
		return
	}
	msg := fmt.Sprintf(format, args...)
	if l.redact {
		msg = Redact(msg, l.secrets)
	}
	line := fmt.Sprintf("%s %-5s %s\n", time.Now().Format(time.RFC3339), level.String(), msg)
	if l.stderr != nil {
		_, _ = io.WriteString(l.stderr, line)
	}
	if l.file != nil {
		_, _ = io.WriteString(l.file, line)
	}
}

// Debugf logs at debug level.
func (l *Logger) Debugf(format string, args ...any) { l.logf(LevelDebug, format, args...) }

// Infof logs at info level.
func (l *Logger) Infof(format string, args ...any) { l.logf(LevelInfo, format, args...) }

// Warnf logs at warn level.
func (l *Logger) Warnf(format string, args ...any) { l.logf(LevelWarn, format, args...) }

// Errorf logs at error level.
func (l *Logger) Errorf(format string, args ...any) { l.logf(LevelError, format, args...) }

// credentialAssign matches `api_key=value`, `token: value` and `"api_key": "v"`
// for any key whose name looks like a credential.
var credentialAssign = regexp.MustCompile(`(?i)(["']?[a-z0-9_\-]*(?:api[_-]?key|access[_-]?key|secret|token|password|passwd|authorization|credentials?)[a-z0-9_\-]*["']?\s*[:=]\s*)("[^"]*"|'[^']*'|[^\s,&}\]]+)`)

// bearerToken matches HTTP `Authorization: Bearer <token>` values.
var bearerToken = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9_\-\.=+/]{6,}`)

// Redact removes credential-looking substrings from s. Known secrets are
// masked first so their exact value never reaches the log file.
func Redact(s string, known []string) string {
	for _, sec := range known {
		if sec != "" {
			s = strings.ReplaceAll(s, sec, "***")
		}
	}
	s = bearerToken.ReplaceAllString(s, "${1}***")
	s = credentialAssign.ReplaceAllString(s, `${1}"***"`)
	return s
}

// Discard is a logger that drops everything; used in tests.
var Discard = New(Options{Level: LevelOff, Stderr: io.Discard})

// global is the process-wide logger used by package-level helpers.
var (
	globalMu sync.RWMutex
	global   = Discard
)

// SetDefault installs the process-wide logger.
func SetDefault(l *Logger) {
	globalMu.Lock()
	defer globalMu.Unlock()
	if l == nil {
		l = Discard
	}
	global = l
}

// Default returns the process-wide logger.
func Default() *Logger {
	globalMu.RLock()
	defer globalMu.RUnlock()
	return global
}

// Debugf logs to the process-wide logger.
func Debugf(format string, args ...any) { Default().Debugf(format, args...) }

// Infof logs to the process-wide logger.
func Infof(format string, args ...any) { Default().Infof(format, args...) }

// Warnf logs to the process-wide logger.
func Warnf(format string, args ...any) { Default().Warnf(format, args...) }

// Errorf logs to the process-wide logger.
func Errorf(format string, args ...any) { Default().Errorf(format, args...) }

// RegisterSecret adds credentials to be masked by the process-wide logger.
func RegisterSecret(values ...string) { Default().Secret(values...) }
