package term

import (
	"strings"
	"unicode"
)

// Editor is a single-line text field that is driven by key events instead of by
// blocking reads. The line editor (ReadLine) is a thin wrapper around it, and a
// full-screen interface can drive the same behaviour from its own event loop.
type Editor struct {
	buf       []rune
	pos       int
	history   *History
	completer Completer
	suggest   func(prefix string) []string
	past      []string
	future    []string
	width     int
	// histIdx is the position in the history while browsing; -1 means fresh.
	histIdx     int
	stash       string
	histStashed bool
	// dirty tells the caller the contents changed, so it can redraw.
	dirty bool
}

// NewEditor creates an empty editor.
func NewEditor() *Editor { return &Editor{width: 80, histIdx: -1} }

// SetWidth sets the wrap width used by Rows.
func (e *Editor) SetWidth(w int) {
	if w > 0 {
		e.width = w
	}
}

// Text returns the current contents.
func (e *Editor) Text() string { return string(e.buf) }

// SetText replaces the contents and puts the caret at the end.
func (e *Editor) SetText(s string) {
	e.buf = []rune(s)
	e.pos = len(e.buf)
	e.dirty = true
}

// Dirty reports whether the contents changed since the flag was last cleared.
func (e *Editor) Dirty() bool { return e.dirty }

// ClearDirty resets the change flag.
func (e *Editor) ClearDirty() { e.dirty = false }

// Cursor returns the caret position in runes.
func (e *Editor) Cursor() int { return e.pos }

// Empty reports whether the field is empty.
func (e *Editor) Empty() bool { return len(e.buf) == 0 }

// Action is what a key press asked the application to do.
type Action int

// Actions a key press can produce.
const (
	// ActionNone means the editor handled the key.
	ActionNone Action = iota
	// ActionSubmit means the line was accepted.
	ActionSubmit
	// ActionCancel means the user pressed Ctrl+C.
	ActionCancel
	// ActionExit means the user pressed Ctrl+D on an empty line.
	ActionExit
	// ActionComplete means Tab was pressed and the completer ran.
	ActionComplete
)

// Result is the outcome of handling one key.
type Result struct {
	Action Action
	// Completion is the text that replaced the buffer, when the completer
	// expanded it.
	Completion string
}

// Handle applies one decoded key press to the editor.
func (e *Editor) Handle(ev Event) Result {
	switch ev.Key {
	case KeyRune:
		if ev.Alt {
			return e.insertRune(ev.Rune)
		}
		return e.insertRune(ev.Rune)
	case KeyEnter:
		e.snapshot()
		return Result{Action: ActionSubmit}
	case KeyCtrlC:
		e.snapshot()
		e.Reset()
		return Result{Action: ActionCancel}
	case KeyCtrlD:
		if len(e.buf) == 0 {
			return Result{Action: ActionExit}
		}
		return e.deleteForward()
	case KeyBackspace:
		return e.deleteBack()
	case KeyDelete:
		return e.deleteForward()
	case KeyLeft:
		if e.pos > 0 {
			e.pos--
		}
	case KeyRight:
		if e.pos < len(e.buf) {
			e.pos++
		}
	case KeyHome, KeyCtrlA:
		e.pos = 0
	case KeyEnd, KeyCtrlE:
		e.pos = len(e.buf)
	case KeyUp:
		e.historyPrev()
	case KeyDown:
		e.historyNext()
	case KeyCtrlK:
		e.buf = e.buf[:e.pos]
	case KeyCtrlU:
		e.buf = append([]rune{}, e.buf[e.pos:]...)
		e.pos = 0
	case KeyCtrlW:
		e.deleteWord()
	case KeyTab:
		return e.complete()
	case KeyCtrlR:
		return Result{}
	}
	return Result{Action: ActionNone}
}

func (e *Editor) insertRune(r rune) Result {
	e.buf = append(e.buf, 0)
	copy(e.buf[e.pos+1:], e.buf[e.pos:])
	e.buf[e.pos] = r
	e.pos++
	e.dirty = true
	return Result{Action: ActionNone}
}

func (e *Editor) deleteBack() Result {
	if e.pos == 0 {
		return Result{}
	}
	e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
	e.pos--
	e.dirty = true
	return Result{}
}

func (e *Editor) deleteForward() Result {
	if e.pos >= len(e.buf) {
		return Result{}
	}
	e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
	e.dirty = true
	return Result{}
}

func (e *Editor) deleteWord() Result {
	i := e.pos
	for i > 0 && unicode.IsSpace(e.buf[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(e.buf[i-1]) {
		i--
	}
	e.buf = append(e.buf[:i], e.buf[e.pos:]...)
	e.pos = i
	e.dirty = true
	return Result{}
}

// snapshot pushes the current text onto the undo stack, so a submitted or
// cleared line can be recalled with Undo.
func (e *Editor) snapshot() {
	if len(e.buf) == 0 {
		return
	}
	if len(e.past) > 0 && e.past[len(e.past)-1] == string(e.buf) {
		return
	}
	e.past = append(e.past, string(e.buf))
	if len(e.past) > 200 {
		e.past = e.past[len(e.past)-200:]
	}
	e.future = nil
}

// Undo restores the previous text. It reports whether anything changed.
func (e *Editor) Undo() bool {
	if len(e.past) == 0 {
		return false
	}
	cur := string(e.buf)
	e.buf = []rune(e.past[len(e.past)-1])
	e.past = e.past[:len(e.past)-1]
	e.future = append(e.future, cur)
	e.pos = len(e.buf)
	e.dirty = true
	return true
}

// Redo re-applies an undone change.
func (e *Editor) Redo() bool {
	if len(e.future) == 0 {
		return false
	}
	cur := string(e.buf)
	e.buf = []rune(e.future[len(e.future)-1])
	e.future = e.future[:len(e.future)-1]
	e.past = append(e.past, cur)
	e.pos = len(e.buf)
	e.dirty = true
	return true
}

// Reset empties the editor.
func (e *Editor) Reset() {
	e.buf = nil
	e.pos = 0
	e.histIdx = -1
	e.stash = ""
	e.histStashed = false
	e.dirty = true
}

// Rows returns the text wrapped to the configured width, plus the caret's row
// and column. Wrapping is counted in columns so wide characters do not shift the
// caret.
func (e *Editor) Rows() (rows []string, caretRow, caretCol int) {
	text := string(e.buf)
	if e.width <= 0 {
		head := string([]rune(text)[:min(runeCol(text), e.pos)])
		return []string{text}, 0, runeWidth(head)
	}
	caretCol = runeWidth(string([]rune(text)[:min(runeCol(text), e.pos)]))
	col := 0
	cur := strings.Builder{}
	for _, r := range text {
		w := runeWidth(string(r))
		if col > 0 && col+w > e.width {
			rows = append(rows, cur.String())
			cur.Reset()
			col = 0
		}
		cur.WriteRune(r)
		col += runeWidth(string(r))
	}
	rows = append(rows, cur.String())
	caretRow = caretCol / e.width
	caretCol = caretCol % e.width
	return rows, caretRow, caretCol
}

// runeCol clamps a rune index to the buffer length.
func runeCol(text string) int {
	return len([]rune(text))
}

// runeWidth counts terminal columns of a string.
func runeWidth(s string) int {
	w := 0
	for _, r := range s {
		if r < 0x20 {
			continue
		}
		w++
	}
	return w
}

// historyPrev walks back through the history, stashing what was being typed.
func (e *Editor) historyPrev() {
	if e.history == nil || e.history.Len() == 0 {
		return
	}
	if !e.histStashed {
		e.stash = string(e.buf)
		e.histStashed = true
	}
	e.histIdx++
	if e.histIdx >= e.history.Len() {
		e.histIdx = e.history.Len() - 1
	}
	e.SetText(e.history.At(e.histIdx))
}

// historyNext walks forward, ending at the stashed text.
func (e *Editor) historyNext() {
	if e.history == nil || e.histIdx < 0 {
		return
	}
	e.histIdx--
	if e.histIdx < 0 {
		e.histIdx = -1
		e.SetText(e.stash)
		e.histStashed = false
		return
	}
	e.SetText(e.history.At(e.histIdx))
}

// SetHistory binds a history to the editor.
func (e *Editor) SetHistory(h *History) { e.history = h }

// SetCompleter binds a completer used by Tab.
func (e *Editor) SetCompleter(c Completer) { e.completer = c }

// SetSuggest binds a suggestion function used for hint lines.
func (e *Editor) SetSuggest(f func(prefix string) []string) { e.suggest = f }

func (e *Editor) complete() Result {
	if e.completer == nil {
		return Result{Action: ActionComplete}
	}
	candidates := e.completer.Complete(string(e.buf))
	if len(candidates) == 0 {
		return Result{Action: ActionComplete}
	}
	if len(candidates) == 1 {
		e.SetText(candidates[0])
		return Result{Action: ActionComplete, Completion: candidates[0]}
	}
	prefix := longestCommonPrefix(candidates)
	if prefix != "" && prefix != string(e.buf) {
		e.SetText(prefix)
		return Result{Action: ActionComplete, Completion: prefix}
	}
	return Result{Action: ActionComplete}
}

// Suggestions returns hint lines for the current text.
func (e *Editor) Suggestions() []string {
	if e.suggest == nil {
		return nil
	}
	return e.suggest(string(e.buf))
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
