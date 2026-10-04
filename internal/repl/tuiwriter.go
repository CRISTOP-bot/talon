package repl

import (
	"regexp"
	"strings"
	"sync"

	tuiapp "github.com/CRISTOP-bot/talon/internal/tuiapp"
)

// sgrPattern matches the escape sequences the printer emits. The full-screen
// interface styles text itself, so command output arrives here with colour
// codes that must be removed before they reach the frame buffer.
var sgrPattern = regexp.MustCompile("\x1b\\[[0-9;?]*[a-zA-Z]")

// tuiWriter turns writes from the ordinary printer into conversation blocks.
//
// Every command in this package prints through the shared printer. In the
// full-screen interface those writes would land in the middle of the frame and
// corrupt it, so they are intercepted, stripped of colour and appended as text.
// This is why slash commands work unchanged in both interfaces.
type tuiWriter struct {
	app *tuiapp.App
	// refresh asks the key loop to repaint. The writer never draws itself: it can
	// be called from a turn's goroutine, and the screen belongs to one loop.
	refresh func()
	// role is the block kind for the next flush.
	role string

	mu    sync.Mutex
	lines []string
	// current is the line being accumulated.
	current strings.Builder
}

// newTUIWriter creates a writer that feeds blocks to the application.
func newTUIWriter(a *tuiapp.App, refresh func(), role string) *tuiWriter {
	return &tuiWriter{app: a, refresh: refresh, role: role}
}

// WithRole returns a writer that labels its blocks as role.
func (w *tuiWriter) WithRole(role string) *tuiWriter {
	return &tuiWriter{app: w.app, refresh: w.refresh, role: role}
}

// Write implements io.Writer.
func (w *tuiWriter) Write(p []byte) (int, error) {
	if w.app == nil {
		return len(p), nil
	}
	text := sgrPattern.ReplaceAllString(string(p), "")
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, r := range text {
		switch r {
		case '\n':
			w.lines = append(w.lines, strings.TrimRight(w.current.String(), " \t"))
			w.current.Reset()
		case '\r':
			// A carriage return only matters for progress lines, which are
			// redrawn rather than appended; ignoring it keeps the text readable.
		default:
			w.current.WriteRune(r)
		}
	}
	if len(w.lines) > 0 {
		w.flushLocked()
	}
	return len(p), nil
}

// Flush pushes any partial line as a block.
func (w *tuiWriter) Flush() {
	if w.app == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.TrimSpace(w.current.String()) != "" {
		w.lines = append(w.lines, strings.TrimRight(w.current.String(), " \t"))
		w.current.Reset()
	}
	w.flushLocked()
}

func (w *tuiWriter) flushLocked() {
	if len(w.lines) == 0 {
		return
	}
	text := strings.TrimRight(strings.Join(w.lines, "\n"), "\n")
	w.lines = nil
	if strings.TrimSpace(text) == "" {
		return
	}
	w.app.AddBlock(w.role, text)
	if w.refresh != nil {
		w.refresh()
	}
}

// RoleError is the block kind for error output.
const (
	roleNotice = "notice"
	roleText   = "text"
)
