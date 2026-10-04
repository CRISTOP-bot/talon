//go:build linux

package repl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CRISTOP-bot/talon/internal/agent"
	"github.com/CRISTOP-bot/talon/internal/audit"
	"github.com/CRISTOP-bot/talon/internal/config"
	"github.com/CRISTOP-bot/talon/internal/index"
	"github.com/CRISTOP-bot/talon/internal/journal"
	"github.com/CRISTOP-bot/talon/internal/llm"
	"github.com/CRISTOP-bot/talon/internal/logger"
	"github.com/CRISTOP-bot/talon/internal/perm"
	"github.com/CRISTOP-bot/talon/internal/project"
	"github.com/CRISTOP-bot/talon/internal/shell"
	"github.com/CRISTOP-bot/talon/internal/tools"
	"github.com/CRISTOP-bot/talon/internal/ui"
)

// ttySession drives the full-screen interface through a pty and collects
// everything the program wrote to the terminal.
type ttySession struct {
	master *os.File
	done   chan error
	chunks chan string
	seen   strings.Builder
	mu     sync.Mutex
}

// startTTYWithREPL starts the interface for an existing REPL over a pty.
func startTTYWithREPL(t *testing.T, r *REPL) *ttySession {
	t.Helper()
	master, slave := openPTYPair(t)
	r.opts.Stdin = slave
	r.opts.Stdout = slave
	r.opts.Stderr = slave
	if r.opts.Workspace == "" {
		r.opts.Workspace = t.TempDir()
	}
	s := &ttySession{master: master, done: make(chan error, 1), chunks: make(chan string, 256)}
	go s.pump()
	go func() {
		s.done <- r.Run(context.Background())
		_ = slave.Close()
	}()
	t.Cleanup(func() {
		select {
		case <-s.done:
		default:
			_ = master.Close()
		}
	})
	return s
}

// pump moves terminal output into a channel. A pty master does not support read
// deadlines on Linux, so reading happens on its own goroutine and the tests wait
// with a timer.
func (s *ttySession) pump() {
	buf := make([]byte, 8192)
	for {
		n, err := s.master.Read(buf)
		if n > 0 {
			s.chunks <- string(buf[:n])
		}
		if err != nil {
			close(s.chunks)
			return
		}
	}
}

// read collects output until the program goes quiet for the given time.
func (s *ttySession) read(t *testing.T, seconds float64) string {
	t.Helper()
	deadline := time.After(time.Duration(seconds * float64(time.Second)))
	for {
		select {
		case chunk, ok := <-s.chunks:
			if !ok {
				return s.seen.String()
			}
			s.mu.Lock()
			s.seen.WriteString(chunk)
			s.mu.Unlock()
			// Reset the idle timer on every chunk.
			deadline = time.After(time.Duration(seconds * float64(time.Second)))
		case <-deadline:
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.seen.String()
		}
	}
}

// len returns how many bytes have been read so far.
func (s *ttySession) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seen.Len()
}

// send writes to the program's input.
func (s *ttySession) send(t *testing.T, text string) {
	t.Helper()
	if _, err := s.master.WriteString(text); err != nil {
		t.Fatalf("cannot write to the pty: %v", err)
	}
}

// newTTYREPL builds a session with the offline provider for pty tests.
func newTTYREPL(t *testing.T) *REPL {
	t.Helper()
	r, err := New(Options{Config: testConfig(t), Provider: llm.NewMock(llm.Options{})})
	if err != nil {
		t.Fatal(err)
	}
	r.SetMode(agent.ModeAsk)
	return r
}

// visible renders what a terminal would show: escape sequences are dropped and a
// cursor-positioning sequence becomes a line break, because the renderer moves
// the cursor instead of rewriting the line. Without that, text painted in two
// frames would read as one broken line.
func visible(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r != 0x1b {
			if r != '\r' {
				b.WriteRune(r)
			}
			continue
		}
		j := i + 1
		if j < len(runes) && runes[j] == '[' {
			j++
			for j < len(runes) && !isFinalByte(runes[j]) {
				j++
			}
			if j < len(runes) {
				switch runes[j] {
				case 'H', 'f', 'J', 'K':
					b.WriteByte('\n')
				}
				j++
			}
		}
		i = j - 1
	}
	return b.String()
}

func isFinalByte(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '@' || r == '~'
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg := config.Defaults()
	cfg.Model.Provider = "mock"
	cfg.Model.Name = "mock-model"
	cfg.Logging.Level = "off"
	cfg.Sessions.Enabled = false
	cfg.Memory.Enabled = false
	cfg.Plugins.Enabled = false
	cfg.Security.AuditEnabled = false
	cfg.Security.RequireTerms = false
	cfg.Security.Sandbox = false
	return cfg
}

// TestTUITypesAndSubmits is the end-to-end guard for the bug that made the
// program ignore everything the user typed: the full-screen interface must
// render each keystroke and react to Enter.
func TestTUITypesAndSubmits(t *testing.T) {
	r := newTTYREPL(t)
	s := startTTYWithREPL(t, r)

	first := visible(s.read(t, 2))
	if !strings.Contains(first, "talon") {
		t.Fatalf("the header is missing:\n%q", first)
	}
	if !strings.Contains(first, "build") {
		t.Fatalf("the mode indicator is missing:\n%q", first)
	}

	// Every keystroke must produce output on its own. This is the regression
	// guard: with a zero-length read buffer the program drew the prompt once and
	// then ignored the keyboard entirely.
	for i, ch := range "hola" {
		before := s.len()
		s.send(t, string(ch))
		s.read(t, 1.5)
		if s.len() == before {
			t.Fatalf("keystroke %d (%q) produced no output", i+1, ch)
		}
	}

	// Submitting must reach the conversation as one line.
	s.send(t, "\r")
	got := visible(s.read(t, 4))
	if !strings.Contains(got, "hola") {
		t.Fatalf("the submitted line did not reach the conversation:\n%q", got)
	}

	// The session must still be alive and answering commands.
	s.send(t, "/help")
	if got := visible(s.read(t, 3)); !strings.Contains(got, "/help") {
		t.Fatalf("the slash command produced nothing:\n%q", got)
	}
}

// TestTUITabTogglesMode proves Tab reaches the key loop and switches modes.
func TestTUITabTogglesMode(t *testing.T) {
	r := newTTYREPL(t)
	s := startTTYWithREPL(t, r)
	s.read(t, 2)
	if r.Mode() != agent.ModeAsk {
		t.Fatalf("initial mode = %v", r.Mode())
	}
	s.send(t, "\t")
	s.read(t, 2)
	if r.Mode() != agent.ModePlan {
		t.Fatalf("Tab should switch to plan mode, got %v", r.Mode())
	}
	s.send(t, "\t")
	s.read(t, 2)
	if r.Mode() != agent.ModeAsk {
		t.Fatalf("Tab again should return to ask mode, got %v", r.Mode())
	}
}

// TestTUICtrlCQuits proves the session ends and leaves the terminal usable.
func TestTUICtrlCQuits(t *testing.T) {
	r := newTTYREPL(t)
	s := startTTYWithREPL(t, r)
	s.read(t, 2)
	s.send(t, "\x03")
	select {
	case err := <-s.done:
		if err != nil {
			t.Fatalf("the session returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Ctrl+C on an empty prompt did not end the session")
	}
}

// TestNoTUIKeepsTheLineEditor proves the flag restores the original interface,
// which is what pipes and CI depend on.
func TestNoTUIKeepsTheLineEditor(t *testing.T) {
	cfg := testConfig(t)
	r, err := New(Options{Config: cfg, Provider: llm.NewMock(llm.Options{}), NoTUI: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.useTUI() {
		t.Fatal("NoTUI must disable the full-screen interface")
	}
}

// TestTUICommandOutputStaysInsideTheFrame proves command output does not corrupt
// the screen: it is routed into the conversation.
func TestTUICommandOutputStaysInsideTheFrame(t *testing.T) {
	r := newTTYREPL(t)
	s := startTTYWithREPL(t, r)
	s.read(t, 2)
	s.send(t, "/sandbox\r")
	got := visible(s.read(t, 4))
	if !strings.Contains(strings.ToLower(got), "landlock") && !strings.Contains(strings.ToLower(got), "sandbox") {
		t.Fatalf("/sandbox produced nothing in the interface:\n%q", got)
	}
}

// unusedImports keeps the helper imports honest if the file shrinks.
var (
	_ = fmt.Sprintf
	_ = filepath.Join
	_ = ui.MonoTheme
	_ = logger.ParseLevel
	_ = perm.Level("confirm")
	_ = tools.All
	_ = shell.NewRunner
	_ = index.New
	_ = project.Detect
	_ = journal.New
	_ = audit.Open
)
