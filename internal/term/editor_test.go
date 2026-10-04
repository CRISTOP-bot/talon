package term

import "testing"

func TestEditorTypingAndSubmit(t *testing.T) {
	e := NewEditor()
	for _, r := range "hola" {
		if got := e.Handle(Event{Key: KeyRune, Rune: r}); got.Action != ActionNone {
			t.Fatalf("typing %q returned %v", r, got.Action)
		}
	}
	if e.Text() != "hola" {
		t.Fatalf("Text() = %q, want %q", e.Text(), "hola")
	}
	if got := e.Handle(Event{Key: KeyEnter}); got.Action != ActionSubmit {
		t.Fatalf("Enter returned %v, want submit", got.Action)
	}
}

func TestEditorCaretMovement(t *testing.T) {
	e := NewEditor()
	e.SetText("abc")
	e.Handle(Event{Key: KeyHome})
	if e.Cursor() != 0 {
		t.Fatalf("Home put the caret at %d", e.Cursor())
	}
	e.Handle(Event{Key: KeyRight})
	e.Handle(Event{Key: KeyRune, Rune: 'X'})
	if e.Text() != "aXbc" {
		t.Fatalf("insert at the caret produced %q", e.Text())
	}
	e.Handle(Event{Key: KeyEnd})
	e.Handle(Event{Key: KeyBackspace})
	if e.Text() != "aXb" {
		t.Fatalf("backspace at the end produced %q", e.Text())
	}
	// The caret is at the end, so Delete must do nothing rather than eat
	// characters from the wrong place.
	e.Handle(Event{Key: KeyDelete})
	if e.Text() != "aXb" {
		t.Fatalf("delete at the end produced %q", e.Text())
	}
	e.Handle(Event{Key: KeyHome})
	e.Handle(Event{Key: KeyDelete})
	if e.Text() != "Xb" {
		t.Fatalf("delete at the caret produced %q", e.Text())
	}
}

func TestEditorControlKeys(t *testing.T) {
	e := NewEditor()
	e.SetText("hello world")
	e.Handle(Event{Key: KeyHome})
	e.Handle(Event{Key: KeyCtrlK})
	if e.Text() != "" {
		t.Fatalf("Ctrl+K should cut to the end of the line, got %q", e.Text())
	}
	// Ctrl+U cuts from the start of the line up to the caret, so with the
	// caret at the beginning there is nothing to cut.
	e.SetText("hello world")
	e.Handle(Event{Key: KeyCtrlA})
	e.Handle(Event{Key: KeyCtrlU})
	if e.Text() != "hello world" {
		t.Fatalf("Ctrl+U with the caret at the start changed the line: %q", e.Text())
	}
	e.Handle(Event{Key: KeyEnd})
	e.Handle(Event{Key: KeyCtrlU})
	if e.Text() != "" {
		t.Fatalf("Ctrl+U should cut to the start, got %q", e.Text())
	}
	e.Handle(Event{Key: KeyCtrlE})
	e.Handle(Event{Key: KeyCtrlW})
	if e.Text() != "" {
		t.Fatalf("Ctrl+W should remove the previous word, got %q", e.Text())
	}
}

func TestEditorCancelAndExit(t *testing.T) {
	e := NewEditor()
	e.SetText("algo")
	if got := e.Handle(Event{Key: KeyCtrlC}); got.Action != ActionCancel {
		t.Fatalf("Ctrl+C returned %v", got.Action)
	}
	if !e.Empty() {
		t.Fatalf("Ctrl+C should clear the line, got %q", e.Text())
	}
	if got := e.Handle(Event{Key: KeyCtrlD}); got.Action != ActionExit {
		t.Fatalf("Ctrl+D on an empty line returned %v", got.Action)
	}
	// With text and the caret at the end, Ctrl+D is a no-op.
	e.SetText("x")
	if got := e.Handle(Event{Key: KeyCtrlD}); got.Action != ActionNone {
		t.Fatalf("Ctrl+D with text returned %v, want none", got.Action)
	}
	if e.Text() != "x" {
		t.Fatalf("Ctrl+D at the end changed the line: %q", e.Text())
	}
	e.Handle(Event{Key: KeyHome})
	e.Handle(Event{Key: KeyCtrlD})
	if e.Text() != "" {
		t.Fatalf("Ctrl+D should delete forward, got %q", e.Text())
	}
}

func TestEditorUndoRestoresSubmittedLine(t *testing.T) {
	e := NewEditor()
	e.SetText("primera peticion")
	e.Handle(Event{Key: KeyEnter}) // submitting snapshots the line
	e.SetText("segunda")
	e.Handle(Event{Key: KeyCtrlC}) // clearing snapshots too
	if !e.Empty() {
		t.Fatalf("Ctrl+C should clear the line, got %q", e.Text())
	}
	if !e.Undo() {
		t.Fatal("Undo did nothing")
	}
	if e.Text() != "segunda" {
		t.Fatalf("Undo restored %q, want %q", e.Text(), "segunda")
	}
	if !e.Undo() {
		t.Fatal("a second Undo did nothing")
	}
	if e.Text() != "primera peticion" {
		t.Fatalf("Undo restored %q, want the first submitted line", e.Text())
	}
	if !e.Redo() {
		t.Fatal("Redo did nothing")
	}
	if e.Text() != "segunda" {
		t.Fatalf("Redo restored %q, want %q", e.Text(), "segunda")
	}
}

func TestEditorHistoryNavigation(t *testing.T) {
	h := NewHistory("", 10)
	h.Add("primero")
	h.Add("segundo")
	e := NewEditor()
	e.SetHistory(h)
	e.Handle(Event{Key: KeyUp})
	if e.Text() != "segundo" {
		t.Fatalf("Up gave %q, want the newest entry", e.Text())
	}
	e.Handle(Event{Key: KeyUp})
	if e.Text() != "primero" {
		t.Fatalf("Up again gave %q", e.Text())
	}
	e.Handle(Event{Key: KeyDown})
	if e.Text() != "segundo" {
		t.Fatalf("Down gave %q", e.Text())
	}
	e.Handle(Event{Key: KeyDown})
	if e.Text() != "" {
		t.Fatalf("walking past the end should restore the stash, got %q", e.Text())
	}
}

func TestEditorCompleter(t *testing.T) {
	e := NewEditor()
	e.SetCompleter(CompleterFunc(func(line string) []string {
		if line == "/se" {
			return []string{"/security", "/session"}
		}
		return nil
	}))
	e.SetText("/se")
	got := e.Handle(Event{Key: KeyTab})
	if got.Action != ActionComplete {
		t.Fatalf("Tab returned %v", got.Action)
	}
	if e.Text() != "/se" {
		t.Fatalf("a shared prefix should be inserted, got %q", e.Text())
	}
	e.SetCompleter(CompleterFunc(func(string) []string { return []string{"/security"} }))
	e.SetText("/se")
	e.Handle(Event{Key: KeyTab})
	if e.Text() != "/security" {
		t.Fatalf("a single candidate should complete, got %q", e.Text())
	}
}

func TestEditorRowsWrapAndCaret(t *testing.T) {
	e := NewEditor()
	e.SetWidth(10)
	e.SetText("0123456789abc")
	rows, row, col := e.Rows()
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %q", len(rows), rows)
	}
	if rows[0] != "0123456789" {
		t.Fatalf("first row = %q", rows[0])
	}
	if row != 1 || col != 3 {
		t.Fatalf("caret at row %d col %d, want 1,3", row, col)
	}
}

func TestEditorRowsWithoutWidth(t *testing.T) {
	e := NewEditor()
	e.SetWidth(0)
	e.SetText("una sola linea")
	rows, row, col := e.Rows()
	if len(rows) != 1 || row != 0 || col != len("una sola linea") {
		t.Fatalf("rows=%q row=%d col=%d", rows, row, col)
	}
}
