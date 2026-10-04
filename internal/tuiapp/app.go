// Package app is the full-screen session: the header, the conversation, the
// editor and the overlays, driven by a single key-event loop.
package app

import (
	"strings"
	"sync"

	"github.com/CRISTOP-bot/talon/internal/term"
	"github.com/CRISTOP-bot/talon/internal/tui"
)

// Block is one entry in the conversation.
type Block struct {
	// Role is "user", "assistant", "tool", "notice" or "error".
	Role string
	Text string
	// Streaming marks text that is still arriving.
	Streaming bool
}

// Handler is what the application needs from the session around it.
type Handler interface {
	// Submit sends a line to the agent.
	Submit(line string)
	// Command runs a slash command.
	Command(line string)
	// Interrupt cancels the running turn.
	Interrupt()
	// Plan reports whether the session is in plan mode.
	Plan() bool
	// SetPlan switches modes.
	SetPlan(plan bool)
	// Files lists project-relative paths for the "@" picker.
	Files() []string
	// Status returns a transient message for the footer.
	Status() (string, tui.Style)
}

// Config configures the application.
type Config struct {
	App     string
	Version string
	Context string
	Palette tui.Palette
	History *term.History
	// Commands maps a slash command to its description.
	Commands map[string]string
	// InitialText pre-fills the editor.
	InitialText string
}

// App is the full-screen session.
type App struct {
	screen *tui.Screen
	cfg    Config
	pal    tui.Palette
	editor *term.Editor
	hist   *term.History

	mu          sync.Mutex
	blocks      []Block
	status      string
	statusStyle tui.Style
	overlay     *tui.Overlay
	approval    *approval
	busy        bool
	handler     Handler
	quit        bool
}

// New creates the application.
func New(screen *tui.Screen, cfg Config, h Handler) *App {
	pal := cfg.Palette
	if pal.Text == (tui.Style{}) {
		pal = tui.DarkPalette()
	}
	a := &App{
		screen:  screen,
		cfg:     cfg,
		pal:     pal,
		editor:  term.NewEditor(),
		hist:    cfg.History,
		handler: h,
	}
	a.editor.SetHistory(cfg.History)
	if cfg.InitialText != "" {
		a.editor.SetText(cfg.InitialText)
	}
	return a
}

// EditorText returns the current prompt contents. It exists for tests and for
// the non-interactive bridge.
func (a *App) EditorText() string { return a.editor.Text() }

// Blocks returns a copy of the conversation, for tests and for /context.
func (a *App) Blocks() []Block {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Block(nil), a.blocks...)
}

// Editor exposes the line editor, so a caller can seed or inspect it.
func (a *App) Editor() *term.Editor { return a.editor }

// Quit reports whether the session should end.
func (a *App) Quit() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.quit
}

// SetBusy records whether the agent is working.
func (a *App) SetBusy(busy bool) {
	a.mu.Lock()
	a.busy = busy
	a.mu.Unlock()
}

// Busy reports whether the agent is working.
func (a *App) Busy() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.busy
}

// AddBlock appends a finished block to the conversation.
func (a *App) AddBlock(role, text string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := len(a.blocks); n > 0 && a.blocks[n-1].Role == role && a.blocks[n-1].Streaming {
		a.blocks[n-1].Text = text
		a.blocks[n-1].Streaming = false
		return
	}
	a.blocks = append(a.blocks, Block{Role: role, Text: text})
}

// Stream appends text to the streaming block, creating it when needed.
func (a *App) Stream(role, chunk string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := len(a.blocks); n > 0 && a.blocks[n-1].Streaming && a.blocks[n-1].Role == role {
		a.blocks[n-1].Text += chunk
		return
	}
	a.blocks = append(a.blocks, Block{Role: role, Text: chunk, Streaming: true})
}

// EndStream marks the streaming block as finished.
func (a *App) EndStream(role string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if n := len(a.blocks); n > 0 && a.blocks[n-1].Streaming && a.blocks[n-1].Role == role {
		a.blocks[n-1].Streaming = false
	}
}

// Reset clears the conversation.
func (a *App) Reset() {
	a.mu.Lock()
	a.blocks = nil
	a.mu.Unlock()
}

// SetStatus shows a transient message in the footer.
func (a *App) SetStatus(s string) {
	a.mu.Lock()
	a.status = s
	a.statusStyle = tui.Style{}
	a.mu.Unlock()
}

// OverlayOpen reports whether a picker is open.
func (a *App) OverlayOpen() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.overlay != nil
}

// Handle applies one decoded key press.
func (a *App) Handle(ev term.Event) {
	// A pending confirmation has priority over everything else.
	if a.handleApprovalKey(ev) {
		a.render()
		return
	}

	// A picker takes every key while it is open.
	a.mu.Lock()
	ov := a.overlay
	a.mu.Unlock()
	if ov != nil {
		a.handleOverlay(ev, ov)
		return
	}

	switch ev.Key {
	case term.KeyCtrlC:
		switch {
		case a.Busy():
			a.handler.Interrupt()
			a.SetStatus("interrumpido")
		case !a.editor.Empty():
			// There is something typed: clear it rather than leaving the session.
			a.editor.Handle(ev)
			a.SetStatus("linea borrada")
		default:
			a.mu.Lock()
			a.quit = true
			a.mu.Unlock()
		}
		a.render()
		return
	case term.KeyCtrlD:
		if a.editor.Empty() {
			a.mu.Lock()
			a.quit = true
			a.mu.Unlock()
		}
		return
	case term.KeyCtrlL:
		a.Reset()
		a.render()
		return
	case term.KeyTab:
		// opencode toggles plan and build with Tab.
		a.handler.SetPlan(!a.handler.Plan())
		a.render()
		return
	case term.KeyRune:
		switch ev.Rune {
		case '@':
			a.openOverlay(tui.FilesOverlay(a.handler.Files()))
			return
		case '/':
			if a.editor.Empty() {
				a.openOverlay(tui.CommandsOverlay(commandNames(a.cfg.Commands), a.cfg.Commands))
				return
			}
		}
	}

	res := a.editor.Handle(ev)
	switch res.Action {
	case term.ActionSubmit:
		line := strings.TrimSpace(a.editor.Text())
		a.editor.Reset()
		if line != "" {
			a.dispatch(line)
		}
		a.render()
	case term.ActionExit:
		a.mu.Lock()
		a.quit = true
		a.mu.Unlock()
	}
	a.render()
}

// dispatch routes a submitted line to the handler.
func (a *App) dispatch(line string) {
	if strings.HasPrefix(line, "/") {
		a.AddBlock("user", line)
		a.handler.Command(line)
		return
	}
	a.AddBlock("user", line)
	a.handler.Submit(line)
}

func commandNames(descriptions map[string]string) []string {
	names := make([]string, 0, len(descriptions))
	for k := range descriptions {
		names = append(names, strings.TrimPrefix(k, "/"))
	}
	sortStrings(names)
	return names
}

func (a *App) openOverlay(ov *tui.Overlay) {
	a.mu.Lock()
	a.overlay = ov
	a.mu.Unlock()
	a.render()
}

func (a *App) closeOverlay() {
	a.mu.Lock()
	a.overlay = nil
	a.mu.Unlock()
}

// handleOverlay routes a key to the open picker.
func (a *App) handleOverlay(ev term.Event, ov *tui.Overlay) {
	switch ev.Key {
	case term.KeyEsc:
		a.closeOverlay()
		a.render()
		return
	}
	handled, accepted, value := ov.Handle(ev)
	if !handled {
		a.closeOverlay()
		a.Handle(ev)
		return
	}
	if accepted {
		insert := ov.InsertAt(value)
		text := a.editor.Text()
		if ov.Kind == tui.OverlayCommands {
			a.closeOverlay()
			a.editor.Reset()
			a.dispatch(insert)
			a.render()
			return
		}
		a.editor.SetText(text + insert)
		a.closeOverlay()
		a.render()
		return
	}
	ov.ApplyQuery()
	a.render()
}

// Render paints one frame.
func (a *App) Render() { a.render() }

func (a *App) render() {
	size := a.screen.Size()
	width, height := size.Width, size.Height

	a.mu.Lock()
	blocks := make([]Block, len(a.blocks))
	copy(blocks, a.blocks)
	status := a.status
	statusStyle := a.statusStyle
	ov := a.overlay
	ap := a.approval
	a.mu.Unlock()

	if msg, st := a.handler.Status(); msg != "" {
		status = msg
		statusStyle = st
	}
	a.editor.SetWidth(width - 4)

	body := a.renderBlocks(blocks, width)
	editorRows, caretRow, caretCol := a.editor.Rows()
	editor := make([]tui.Line, 0, len(editorRows))
	for _, row := range editorRows {
		editor = append(editor, tui.Line{
			{Text: "❯ ", Style: a.pal.Accent},
			{Text: row, Style: a.pal.Text},
		})
	}

	hintText := hintLine(ov, a.Busy())
	if ap != nil {
		hintText = "y allow · a always · n deny"
	}
	hint := tui.Text(a.pal.Muted, hintText)
	layout := tui.Layout{
		Header:      []tui.Line{tui.HeaderLine(a.pal, width, a.cfg.App, a.cfg.Version, a.cfg.Context)},
		Body:        body,
		Editor:      editor,
		FooterHint:  hint,
		Mode:        tui.ModeLabel(a.pal, a.handler.Plan()),
		Status:      status,
		StatusStyle: statusStyle,
	}
	switch {
	case ov != nil:
		layout.Overlay = ov.Lines(a.pal, width, height)
	case ap != nil:
		layout.Overlay = approvalLines(a.pal, ap, width)
	}
	lines, styles, _, cursorY := tui.Render(a.pal, layout, width, height)

	a.screen.ShowCursor(true)
	a.screen.SetCursor(caretCol+2, cursorY+caretRow)
	if err := a.screen.Draw(lines, styles); err != nil {
		a.SetStatus("error de pantalla: " + err.Error())
	}
}

// renderBlocks turns the conversation into styled lines.
func (a *App) renderBlocks(blocks []Block, width int) []tui.Line {
	var out []tui.Line
	for _, b := range blocks {
		switch b.Role {
		case "user":
			out = append(out, tui.WrapText(a.pal.User, b.Text, width-4)...)
			out = append(out, tui.Line{})
		case "assistant":
			lines := tui.WrapText(a.pal.Assistant, b.Text, width-2)
			for i := range lines {
				lines[i] = prepend(tui.Span{Text: "  ", Style: a.pal.Muted}, lines[i])
			}
			out = append(out, lines...)
			out = append(out, tui.Line{})
		case "tool":
			out = append(out, tui.Bullet(a.pal, strings.TrimSpace(b.Text)))
		case "error":
			lines := tui.WrapText(a.pal.Danger, b.Text, width-4)
			for i := range lines {
				lines[i] = prepend(tui.Span{Text: "  ✗ ", Style: a.pal.Danger}, lines[i])
			}
			out = append(out, lines...)
			out = append(out, tui.Line{})
		case "notice":
			for _, l := range tui.WrapText(a.pal.Warning, b.Text, width-4) {
				out = append(out, prepend(tui.Span{Text: "  ! ", Style: a.pal.Warning}, l))
			}
		default:
			out = append(out, tui.WrapText(a.pal.Text, b.Text, width-2)...)
		}
	}
	return out
}

func prepend(s tui.Span, l tui.Line) tui.Line {
	return append(tui.Line{s}, l...)
}

func hintLine(ov *tui.Overlay, busy bool) string {
	if ov != nil {
		return ov.Describe()
	}
	if busy {
		return "working… ctrl+c interrupt"
	}
	return "tab plan/build · @ file · / command · ctrl+l clear · ctrl+c exit"
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// approval is a pending confirmation shown above the conversation.
type approval struct {
	description string
	tool        string
	risk        string
	command     string
	path        string
	// choice is 0 while unanswered, then 1 allow, 2 always, 3 deny.
	choice int
	done   chan struct{}
	once   sync.Once
}

// RequestApproval shows a confirmation panel and waits for the answer. It is
// called from the agent's goroutine while the key loop keeps running, so the
// user can still see the conversation.
func (a *App) RequestApproval(description, tool, risk, command, path string) bool {
	ap := &approval{
		description: description,
		tool:        tool,
		risk:        risk,
		command:     command,
		path:        path,
		done:        make(chan struct{}),
	}
	a.mu.Lock()
	a.approval = ap
	a.mu.Unlock()
	a.render()
	<-ap.done
	a.mu.Lock()
	choice := ap.choice
	a.approval = nil
	a.mu.Unlock()
	a.render()
	return choice == 1 || choice == 2
}

// resolveApproval records the answer and wakes the waiting goroutine.
func (a *App) resolveApproval(choice int) {
	a.mu.Lock()
	ap := a.approval
	a.mu.Unlock()
	if ap == nil {
		return
	}
	ap.once.Do(func() {
		ap.choice = choice
		close(ap.done)
	})
}

// handleApprovalKey answers a pending confirmation.
func (a *App) handleApprovalKey(ev term.Event) bool {
	a.mu.Lock()
	ap := a.approval
	a.mu.Unlock()
	if ap == nil {
		return false
	}
	switch ev.Key {
	case term.KeyRune:
		switch ev.Rune {
		case 'y', 'Y':
			a.resolveApproval(1)
			return true
		case 'a', 'A':
			a.resolveApproval(2)
			return true
		case 'n', 'N', 'q':
			a.resolveApproval(3)
			return true
		}
	case term.KeyEnter:
		a.resolveApproval(1)
		return true
	case term.KeyEsc, term.KeyCtrlC:
		// A confirmation must never be dismissed by accident: Ctrl+C only
		// interrupts the turn, which cancels the action too.
		a.resolveApproval(3)
		return true
	}
	return false
}

// approvalLines renders the confirmation panel.
func approvalLines(p tui.Palette, ap *approval, width int) []tui.Line {
	title := "confirm"
	if ap.tool != "" {
		title = "confirm " + ap.tool
	}
	body := []tui.Line{}
	if ap.description != "" {
		body = append(body, tui.Text(p.Text, ap.description))
	}
	meta := tui.Line{}
	if ap.risk != "" {
		meta = append(meta, tui.Span{Text: "risk ", Style: p.Muted}, tui.Span{Text: ap.risk, Style: p.Warning})
	}
	if ap.path != "" {
		meta = append(meta, tui.Span{Text: "  path ", Style: p.Muted}, tui.Span{Text: ap.path, Style: p.Code})
	}
	if len(meta) > 0 {
		body = append(body, meta)
	}
	if ap.command != "" {
		for _, l := range tui.WrapText(p.Code, ap.command, width-6) {
			body = append(body, l)
		}
	}
	return tui.Box(p, width, title, body)
}
