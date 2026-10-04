package app

import (
	"strings"
	"testing"

	"github.com/CRISTOP-bot/talon/internal/term"
	"github.com/CRISTOP-bot/talon/internal/tui"
)

// fakeHandler records what the application asked the session to do.
type fakeHandler struct {
	submitted  []string
	commands   []string
	interrupts int
	plan       bool
	files      []string
	status     string
	statusSty  tui.Style
}

func (f *fakeHandler) Submit(line string)          { f.submitted = append(f.submitted, line) }
func (f *fakeHandler) Command(line string)         { f.commands = append(f.commands, line) }
func (f *fakeHandler) Interrupt()                  { f.interrupts++ }
func (f *fakeHandler) Plan() bool                  { return f.plan }
func (f *fakeHandler) SetPlan(plan bool)           { f.plan = plan }
func (f *fakeHandler) Files() []string             { return f.files }
func (f *fakeHandler) Status() (string, tui.Style) { return f.status, f.statusSty }

func newTestApp(t *testing.T, h Handler) (*App, *strings.Builder) {
	t.Helper()
	buf := &strings.Builder{}
	screen := tui.NewScreen(buf, tui.Size{Width: 60, Height: 12})
	// The screen is not started, so Draw is a no-op and the app can be driven
	// without a terminal.
	a := New(screen, Config{
		App:     "talon",
		Version: "test",
		Context: "/tmp/proyecto",
		Commands: map[string]string{
			"security": "show the security controls",
			"session":  "manage sessions",
			"tools":    "list tools",
		},
	}, h)
	return a, buf
}

func typeText(a *App, text string) {
	for _, r := range text {
		a.Handle(term.Event{Key: term.KeyRune, Rune: r})
	}
}

func TestTypingAndSubmit(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	typeText(a, "hola mundo")
	a.Handle(term.Event{Key: term.KeyEnter})
	if len(h.submitted) != 1 || h.submitted[0] != "hola mundo" {
		t.Fatalf("submitted = %v", h.submitted)
	}
	if a.EditorText() != "" {
		t.Errorf("the editor should be empty after submitting, got %q", a.EditorText())
	}
}

func TestSlashCommandGoesToCommandHandler(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	typeText(a, "/security")
	a.Handle(term.Event{Key: term.KeyEnter})
	if len(h.commands) != 1 || h.commands[0] != "/security" {
		t.Fatalf("commands = %v", h.commands)
	}
	if len(h.submitted) != 0 {
		t.Fatalf("a slash command must not be submitted as a prompt: %v", h.submitted)
	}
}

func TestTabTogglesPlanMode(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	a.Handle(term.Event{Key: term.KeyTab})
	if !h.Plan() {
		t.Fatal("Tab should switch to plan mode")
	}
	a.Handle(term.Event{Key: term.KeyTab})
	if h.Plan() {
		t.Fatal("Tab again should return to build mode")
	}
}

func TestCtrlCInterruptsWhileBusyAndQuitsOtherwise(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	// With text pending, Ctrl+C clears the line instead of leaving.
	typeText(a, "algo")
	a.Handle(term.Event{Key: term.KeyCtrlC})
	if a.Quit() {
		t.Fatal("Ctrl+C with text pending must not quit")
	}
	if a.EditorText() != "" {
		t.Fatalf("Ctrl+C should have cleared the line, got %q", a.EditorText())
	}
	if h.interrupts != 0 {
		t.Fatal("Ctrl+C while idle must not interrupt")
	}

	a.SetBusy(true)
	a.Handle(term.Event{Key: term.KeyCtrlC})
	if h.interrupts != 1 {
		t.Fatalf("interrupts = %d, want 1", h.interrupts)
	}
	if a.Quit() {
		t.Fatal("Ctrl+C while busy must not quit")
	}

	a.SetBusy(false)
	a.Handle(term.Event{Key: term.KeyCtrlC})
	if !a.Quit() {
		t.Fatal("Ctrl+C while idle and empty should quit")
	}
}

func TestCtrlDQuitsOnlyWhenEmpty(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	typeText(a, "texto")
	a.Handle(term.Event{Key: term.KeyCtrlD})
	if a.Quit() {
		t.Fatal("Ctrl+D with text must not quit")
	}
	a.editor.Reset()
	a.Handle(term.Event{Key: term.KeyCtrlD})
	if !a.Quit() {
		t.Fatal("Ctrl+D on an empty line should quit")
	}
}

func TestAtOpensFilePickerAndInsertsPath(t *testing.T) {
	h := &fakeHandler{files: []string{"internal/tui/view.go", "internal/tui/screen.go", "README.md"}}
	a, _ := newTestApp(t, h)
	typeText(a, "revisa ")
	a.Handle(term.Event{Key: term.KeyRune, Rune: '@'})
	if !a.OverlayOpen() {
		t.Fatal("@ should open the file picker")
	}
	typeText(a, "screen")
	a.Handle(term.Event{Key: term.KeyEnter})
	if a.OverlayOpen() {
		t.Fatal("Enter should close the picker")
	}
	text := a.EditorText()
	if !strings.Contains(text, "@internal/tui/screen.go") {
		t.Fatalf("the chosen path was not inserted: %q", text)
	}
}

func TestSlashAtStartOpensCommandPalette(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	a.Handle(term.Event{Key: term.KeyRune, Rune: '/'})
	if !a.OverlayOpen() {
		t.Fatal("/ on an empty line should open the palette")
	}
	typeText(a, "secur")
	a.Handle(term.Event{Key: term.KeyEnter})
	if a.OverlayOpen() {
		t.Fatal("Enter should close the palette")
	}
	if len(h.commands) != 1 || h.commands[0] != "/security" {
		t.Fatalf("commands = %v, want /security", h.commands)
	}
}

func TestSlashInsideTextIsLiteral(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	typeText(a, "arregla /security")
	if a.OverlayOpen() {
		t.Fatal("/ inside a sentence must not open the palette")
	}
	if !strings.Contains(a.EditorText(), "/security") {
		t.Fatalf("the text was altered: %q", a.EditorText())
	}
}

func TestEscapeClosesOverlayWithoutInserting(t *testing.T) {
	h := &fakeHandler{files: []string{"a.go", "b.go"}}
	a, _ := newTestApp(t, h)
	typeText(a, "@")
	a.Handle(term.Event{Key: term.KeyEsc})
	if a.OverlayOpen() {
		t.Fatal("Esc should close the picker")
	}
	if a.EditorText() != "" {
		t.Fatalf("Esc must not insert anything, got %q", a.EditorText())
	}
}

func TestBackspaceOnEmptyQueryClosesOverlay(t *testing.T) {
	h := &fakeHandler{files: []string{"a.go"}}
	a, _ := newTestApp(t, h)
	a.Handle(term.Event{Key: term.KeyRune, Rune: '@'})
	// Backspacing over an empty query means "get me out of here".
	a.Handle(term.Event{Key: term.KeyBackspace})
	if a.OverlayOpen() {
		t.Fatal("backspace on an empty query should close the picker")
	}
	if a.EditorText() != "" {
		t.Fatalf("nothing should have been inserted, got %q", a.EditorText())
	}
}

func TestBackspaceInsideQueryEditsIt(t *testing.T) {
	h := &fakeHandler{files: []string{"alpha.go", "beta.go"}}
	a, _ := newTestApp(t, h)
	a.Handle(term.Event{Key: term.KeyRune, Rune: '@'})
	typeText(a, "alp")
	a.Handle(term.Event{Key: term.KeyBackspace})
	if !a.OverlayOpen() {
		t.Fatal("backspace inside the query must not close the picker")
	}
	a.Handle(term.Event{Key: term.KeyEnter})
	if !strings.Contains(a.EditorText(), "@alpha.go") {
		t.Fatalf("got %q, want alpha.go", a.EditorText())
	}
}

func TestArrowKeysMoveTheSelection(t *testing.T) {
	h := &fakeHandler{files: []string{"a.go", "b.go", "c.go"}}
	a, _ := newTestApp(t, h)
	a.Handle(term.Event{Key: term.KeyRune, Rune: '@'})
	a.Handle(term.Event{Key: term.KeyDown})
	a.Handle(term.Event{Key: term.KeyDown})
	a.Handle(term.Event{Key: term.KeyDown})
	a.Handle(term.Event{Key: term.KeyEnter})
	if !strings.Contains(a.EditorText(), "@c.go") {
		t.Fatalf("selection did not stop at the last item: %q", a.EditorText())
	}
}

func TestStreamingUpdatesTheLastBlock(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	a.Stream("assistant", "par")
	a.Stream("assistant", "cial")
	a.EndStream("assistant")
	blocks := a.Blocks()
	if len(blocks) != 1 {
		t.Fatalf("expected one block, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Text != "parcial" {
		t.Fatalf("streamed text = %q", blocks[0].Text)
	}
}

func TestAddBlockReplacesStreamingBlock(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	a.Stream("assistant", "a")
	a.AddBlock("assistant", "final")
	blocks := a.Blocks()
	if len(blocks) != 1 || blocks[0].Text != "final" {
		t.Fatalf("blocks = %+v", blocks)
	}
}

func TestCtrlLClearsTheConversation(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	a.AddBlock("user", "algo")
	a.Handle(term.Event{Key: term.KeyCtrlL})
	if len(a.Blocks()) != 0 {
		t.Fatalf("Ctrl+L should clear the conversation, got %+v", a.Blocks())
	}
}

func TestEmptySubmitIsIgnored(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	a.Handle(term.Event{Key: term.KeyEnter})
	if len(h.submitted) != 0 || len(h.commands) != 0 {
		t.Fatalf("an empty line must not be dispatched: %v %v", h.submitted, h.commands)
	}
}

func TestRenderProducesAFrameWithoutPanicking(t *testing.T) {
	h := &fakeHandler{status: "listo"}
	a, buf := newTestApp(t, h)
	a.AddBlock("user", "hola")
	a.Stream("assistant", "esto es una respuesta larga que deberia ocupar varias lineas del panel")
	a.render()
	if buf.Len() != 0 {
		// The screen was never started, so nothing should reach the writer.
		t.Fatalf("a non-started screen wrote %d bytes", buf.Len())
	}
}

func TestHandleResizeKeepsTheFrameValid(t *testing.T) {
	h := &fakeHandler{}
	a, _ := newTestApp(t, h)
	a.AddBlock("user", "hola")
	for _, size := range []tui.Size{{Width: 20, Height: 6}, {Width: 200, Height: 60}, {Width: 1, Height: 1}} {
		a.screen.SetSize(size)
		a.render()
		if a.screen.Size() != size {
			t.Fatalf("size = %+v, want %+v", a.screen.Size(), size)
		}
	}
}
