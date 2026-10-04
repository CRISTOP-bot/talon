package repl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/CRISTOP-bot/talon/internal/agent"
	appinfo "github.com/CRISTOP-bot/talon/internal/app"
	"github.com/CRISTOP-bot/talon/internal/errs"
	"github.com/CRISTOP-bot/talon/internal/paths"
	"github.com/CRISTOP-bot/talon/internal/term"
	"github.com/CRISTOP-bot/talon/internal/tui"
	tuiapp "github.com/CRISTOP-bot/talon/internal/tuiapp"
)

// tuiHandler adapts the REPL to the full-screen application.
type tuiHandler struct {
	r   *REPL
	ctx context.Context
}

func (h *tuiHandler) Submit(line string) { h.r.runTurnAsync(h.ctx, line) }

func (h *tuiHandler) Command(line string) {
	if h.r.handleSlash(h.ctx, line) {
		return
	}
	h.r.runTurnAsync(h.ctx, line)
}

func (h *tuiHandler) Interrupt() {
	if h.r.currentTurnCancel != nil {
		h.r.currentTurnCancel()
	}
}

func (h *tuiHandler) Plan() bool { return h.r.agent.Mode() == agent.ModePlan }

func (h *tuiHandler) SetPlan(plan bool) {
	if plan {
		h.r.SetMode(agent.ModePlan)
		return
	}
	h.r.SetMode(agent.ModeAsk)
}

func (h *tuiHandler) Files() []string {
	entries := h.r.index.Entries()
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		// Hidden files and directories are noise in a picker meant for source.
		if strings.HasPrefix(e.Path, ".") || strings.Contains(e.Path, "/.") {
			continue
		}
		files = append(files, e.Path)
	}
	sort.Strings(files)
	// The picker is a convenience, not a file manager: a few hundred entries are
	// plenty and the list stays responsive.
	if len(files) > 800 {
		files = files[:800]
	}
	return files
}

func (h *tuiHandler) Status() (string, tui.Style) {
	if h.r.notReady != nil {
		return "sin proveedor configurado", tui.Style{Fg: tui.Red}
	}
	return "", tui.Style{}
}

// runTUI runs the full-screen session. It is the default when stdin and stdout
// are a terminal; anything else keeps the line editor, which behaves correctly
// in pipes, logs and CI.
func (r *REPL) runTUI(ctx context.Context) error {
	screen := tui.NewScreen(r.opts.Stdout, tui.Size{})
	if err := screen.Start(); err != nil {
		return fmt.Errorf("cannot start the full-screen interface: %w", err)
	}
	defer screen.Stop()

	pal := tui.DarkPalette()
	if r.cfg.UI.Theme == "light" {
		pal = tui.LightPalette()
	}
	application := tuiapp.New(screen, tuiapp.Config{
		App:      "talon",
		Version:  appinfo.Version,
		Context:  r.tuiContextLabel(),
		Palette:  pal,
		History:  term.NewHistory(paths.HistoryPath(), 1000),
		Commands: slashDescriptions,
	}, &tuiHandler{r: r, ctx: ctx})
	r.tuiApp = application
	defer func() { r.tuiApp = nil }()
	// From here on the shared printer writes into the conversation instead of
	// straight to the terminal, so every existing command keeps working.
	// Ordinary output is plain text; failures are marked so they stand out.
	r.tuiOut = newTUIWriter(application, r.requestRefresh, roleText)
	r.tuiErr = newTUIWriter(application, r.requestRefresh, roleNotice)
	r.pr.SetOut(r.tuiOut, r.tuiErr)

	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	defer signal.Stop(resize)
	go func() {
		for range resize {
			if w, h, err := term.Size(r.opts.Stdin); err == nil {
				screen.SetSize(tui.Size{Width: w, Height: h})
				application.Render()
			}
		}
	}()

	application.Render()

	// The key loop owns the terminal, so turns and approvals run on their own
	// goroutines and report back through the application.
	keys := make(chan term.Event, 64)
	stop := make(chan struct{})
	go readKeys(r.opts.Stdin, keys, stop)
	defer close(stop)

	if strings.TrimSpace(r.opts.InitialPrompt) != "" {
		application.Editor().SetText(strings.TrimSpace(r.opts.InitialPrompt))
		application.Render()
	}

	for !application.Quit() {
		select {
		case <-ctx.Done():
			return nil
		case ev := <-keys:
			application.Handle(ev)
			application.Render()
		case <-r.tuiRefresh():
			application.Render()
		}
	}
	r.waitForTurn()
	return nil
}

// tuiRefresh returns a channel that ticks whenever background work changed the
// view, so the screen updates without a key press.
func (r *REPL) tuiRefresh() <-chan struct{} {
	if r.refreshCh == nil {
		r.refreshCh = make(chan struct{}, 1)
	}
	return r.refreshCh
}

// requestRefresh asks the key loop to repaint.
func (r *REPL) requestRefresh() {
	if r.refreshCh == nil {
		return
	}
	select {
	case r.refreshCh <- struct{}{}:
	default:
	}
}

// runTurnAsync runs one turn without blocking the key loop.
func (r *REPL) runTurnAsync(ctx context.Context, input string) {
	if r.tuiApp != nil {
		r.tuiApp.SetBusy(true)
		r.tuiApp.Render()
	}
	r.turns.Add(1)
	go func() {
		defer r.turns.Done()
		defer func() {
			if rec := recover(); rec != nil {
				if r.tuiApp != nil {
					r.tuiApp.AddBlock("error", fmt.Sprintf("panic interno: %v", rec))
				}
			}
			if r.tuiApp != nil {
				r.tuiApp.SetBusy(false)
			}
			r.requestRefresh()
		}()
		if err := r.turn(ctx, input); err != nil {
			r.reportTurnToTUI(err)
		}
	}()
}

// waitForTurn blocks until every running turn has finished, so shutdown does not
// cut a turn in half.
func (r *REPL) waitForTurn() {
	done := make(chan struct{})
	go func() {
		r.turns.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

// reportTurnToTUI shows a turn failure inside the full-screen view.
func (r *REPL) reportTurnToTUI(err error) {
	if r.tuiApp == nil {
		return
	}
	r.tuiApp.AddBlock("error", errs.User(err))
}

// readKeys decodes terminal input into key events. It is the full-screen
// counterpart of the line editor's read loop.
func readKeys(f *os.File, out chan<- term.Event, stop <-chan struct{}) {
	if state, err := term.MakeRaw(f); err == nil {
		defer func() { _ = state.Restore() }()
	}
	buf := make([]byte, 256)
	for {
		select {
		case <-stop:
			return
		default:
		}
		n, err := f.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			for len(chunk) > 0 {
				ev, size, derr := term.Decode(chunk)
				if derr != nil {
					break
				}
				chunk = chunk[size:]
				select {
				case out <- ev:
				case <-stop:
					return
				}
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				select {
				case out <- term.Event{Key: term.KeyCtrlD}:
				case <-stop:
				}
			}
			return
		}
	}
}

// versionLabel is the string shown in the header.
func (r *REPL) versionLabel() string { return appinfo.Version }

// tuiContextLabel is the right-hand side of the header.
func (r *REPL) tuiContextLabel() string {
	parts := []string{r.cfg.EffectiveModel()}
	if r.cfg.Permissions.Level != "" {
		parts = append(parts, r.cfg.Permissions.Level)
	}
	parts = append(parts, shortPath(r.opts.Workspace))
	return strings.Join(parts, " · ")
}

// shortPath trims the home directory from a path for display.
func shortPath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home) {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}

// useTUI reports whether the full-screen interface can run. It requires a
// terminal on both stdin and stdout, and it is disabled with --no-tui.
func (r *REPL) useTUI() bool {
	if r.opts.NoTUI {
		return false
	}
	if r.opts.Stdin == nil || r.opts.Stdout == nil {
		return false
	}
	if r.opts.Stdout == io.Discard || r.opts.Stderr == io.Discard {
		return false
	}
	out, ok := r.opts.Stdout.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(r.opts.Stdin) && term.IsTerminal(out)
}

// onTUIEvent turns an agent event into a conversation block.
func (r *REPL) onTUIEvent(ev agent.Event) {
	a := r.tuiApp
	if a == nil {
		return
	}
	switch ev.Kind {
	case agent.EventText:
		a.Stream("assistant", ev.Text)
	case agent.EventToolStart:
		a.AddBlock("tool", formatToolStart(ev.Tool))
	case agent.EventToolApproval:
		if ev.Text != "" && strings.Contains(ev.Text, "denied") {
			a.AddBlock("notice", ev.Tool+": "+ev.Text)
		}
	case agent.EventToolResult:
		r.flushStream()
		if ev.Err != nil {
			a.AddBlock("error", ev.Tool+": "+errs.User(ev.Err))
			break
		}
		if res := ev.Result; res != nil {
			if res.Diff != nil {
				r.pendingDiffs = append(r.pendingDiffs, *res.Diff)
			}
			a.AddBlock("tool", res.Display)
		}
	case agent.EventTurnEnd:
		a.EndStream("assistant")
	}
	r.requestRefresh()
}
