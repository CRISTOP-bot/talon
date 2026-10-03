package term

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
)

// Completer supplies tab-completion candidates for the current input.
type Completer interface {
	Complete(line string) []string
}

// CompleterFunc adapts a function to Completer.
type CompleterFunc func(line string) []string

// Complete implements Completer.
func (f CompleterFunc) Complete(line string) []string { return f(line) }

// ErrInterrupt is returned when the user presses Ctrl+C.
var ErrInterrupt = errors.New("interrupted")

// Readline is a small line editor with history, completion and multi-line
// continuation. It only requires raw mode, not a full-screen TUI framework.
type Readline struct {
	In        *os.File
	Out       io.Writer
	History   *History
	Completer Completer
	// Suggest renders hint lines under the prompt; it may be nil.
	Suggest func(prefix string) []string
	// NoSuggestions disables the hint lines even when Suggest is set.
	NoSuggestions bool
	// OnSubmit is called after a line is accepted, before it is returned.
	OnSubmit func(line string)
	// Prompt overrides the default ask prompt.
	Prompt Prompt

	last  int // number of screen lines drawn by the previous refresh
	plain *bufio.Reader
}

// NewReadline creates an editor bound to the given streams.
func NewReadline(in *os.File, out io.Writer, hist *History) *Readline {
	return &Readline{In: in, Out: out, History: hist}
}

type editor struct {
	rl        *Readline
	buf       []rune
	pos       int
	prompt    string
	sug       []string
	stash     string // buffer saved when a search or completion starts
	histIdx   int    // -1 means "editing a fresh line"
	matches   []string
	matchAt   int
	search    string
	searching bool
	saved     []rune // line to restore after an incremental search is aborted
	savedPos  int
}

// ReadLine prompts and returns the submitted line.
func (r *Readline) ReadLine(prompt string) (string, error) {
	if !IsTerminal(r.In) {
		return r.readLinePlain(prompt)
	}
	state, err := MakeRaw(r.In)
	if err != nil {
		return r.readLinePlain(prompt)
	}
	defer func() { _ = state.Restore() }()

	// Hide the cursor while we own the line, restore it before returning.
	fmt.Fprint(r.Out, "\x1b[?25l")
	defer fmt.Fprint(r.Out, "\x1b[?25h")

	e := &editor{rl: r, prompt: prompt, histIdx: -1}
	line, err := e.loop()
	e.clear()
	if err != nil {
		return "", err
	}
	if r.History != nil {
		r.History.Add(line)
	}
	if r.OnSubmit != nil {
		r.OnSubmit(line)
	}
	return line, nil
}

func (r *Readline) readLinePlain(prompt string) (string, error) {
	in, out := r.In, r.Out
	if _, err := io.WriteString(out, prompt); err != nil {
		return "", err
	}
	br := r.plain
	if br == nil {
		br = bufio.NewReader(in)
		r.plain = br
	}
	line, err := br.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && line == "" {
			return "", io.EOF
		}
		if !errors.Is(err, io.EOF) {
			return "", err
		}
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (e *editor) loop() (string, error) {
	// The buffer must have a length: a zero-length slice makes read(2) return
	// (0, nil) immediately, which spins forever and never sees a keystroke.
	buf := make([]byte, 256)
	e.redraw()
	for {
		n, err := e.rl.In.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			done, interrupted := e.handleChunk(chunk)
			switch {
			case interrupted:
				return "", ErrInterrupt
			case done:
				return e.line(), nil
			}
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return "", io.EOF
			}
			return "", err
		}
	}
}

func (e *editor) line() string { return string(e.buf) }

// handleChunk consumes decoded events. It reports whether the line was
// accepted and whether the user interrupted with Ctrl+C.
func (e *editor) handleChunk(chunk []byte) (submitted, interrupted bool) {
	for len(chunk) > 0 {
		ev, n, err := Decode(chunk)
		if err != nil {
			return submitted, interrupted
		}
		chunk = chunk[n:]
		switch ev.Key {
		case KeyEnter:
			if !e.searching && NeedsContinuation(string(e.buf)) {
				e.insert('\n')
				e.redraw()
				continue
			}
			return true, false
		case KeyCtrlC:
			if e.searching {
				e.endSearch(false)
				e.redraw()
				continue
			}
			e.reset()
			e.redraw()
			return false, true
		case KeyCtrlD:
			if len(e.buf) == 0 {
				return true, false
			}
			e.insert(0)
			e.redraw()
		case KeyBackspace:
			e.deleteBefore()
			e.redraw()
		case KeyDelete:
			e.deleteAt()
			e.redraw()
		case KeyLeft:
			if e.pos > 0 {
				e.pos--
				e.redraw()
			}
		case KeyRight:
			if e.pos < len(e.buf) {
				e.pos++
				e.redraw()
			}
		case KeyHome:
			e.pos = 0
			e.redraw()
		case KeyEnd:
			e.pos = len(e.buf)
			e.redraw()
		case KeyUp:
			e.historyPrev()
			e.redraw()
		case KeyDown:
			e.historyNext()
			e.redraw()
		case KeyPageUp:
			e.histIdx = -1
			e.setBuf(e.rl.History.At(0))
			e.redraw()
		case KeyCtrlA:
			e.pos = 0
			e.redraw()
		case KeyCtrlE:
			e.pos = len(e.buf)
			e.redraw()
		case KeyCtrlK:
			e.buf = e.buf[:e.pos]
			e.redraw()
		case KeyCtrlU:
			e.buf = e.buf[e.pos:]
			e.pos = 0
			e.redraw()
		case KeyCtrlW:
			e.deleteWord()
			e.redraw()
		case KeyCtrlL:
			fmt.Fprint(e.rl.Out, "\x1b[H\x1b[2J")
			e.rl.last = 1
			e.redraw()
		case KeyCtrlR:
			e.startSearch()
		case KeyTab:
			e.complete()
		case KeyEsc:
			if e.searching {
				e.endSearch(false)
				e.redraw()
			}
		case KeyRune:
			e.insert(ev.Rune)
			if e.searching {
				e.search = string(e.buf)
				e.applySearch()
			} else {
				e.updateSuggestions()
			}
			e.redraw()
		}
	}
	return submitted, interrupted
}

func (e *editor) reset() {
	e.buf = nil
	e.pos = 0
	e.histIdx = -1
	e.sug = nil
	e.matches = nil
	e.matchAt = 0
}

func (e *editor) setBuf(s string) {
	e.buf = []rune(s)
	e.pos = len(e.buf)
}

func (e *editor) insert(r rune) {
	e.buf = append(e.buf, 0)
	copy(e.buf[e.pos+1:], e.buf[e.pos:])
	e.buf[e.pos] = r
	e.pos++
}

func (e *editor) deleteBefore() {
	if e.pos == 0 {
		return
	}
	e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
	e.pos--
}

func (e *editor) deleteAt() {
	if e.pos >= len(e.buf) {
		return
	}
	e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
}

func (e *editor) deleteWord() {
	i := e.pos
	for i > 0 && unicode.IsSpace(e.buf[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(e.buf[i-1]) {
		i--
	}
	e.buf = append(e.buf[:i], e.buf[e.pos:]...)
	e.pos = i
}

func (e *editor) historyPrev() {
	if e.rl.History == nil {
		return
	}
	if e.histIdx == -1 {
		e.stash = string(e.buf)
	}
	e.histIdx++
	if s := e.rl.History.At(e.histIdx); s != "" || e.histIdx < e.rl.History.Len() {
		e.setBuf(s)
	}
	if e.histIdx >= e.rl.History.Len() {
		e.histIdx = e.rl.History.Len()
	}
}

func (e *editor) historyNext() {
	if e.rl.History == nil || e.histIdx < 0 {
		return
	}
	e.histIdx--
	if e.histIdx < 0 {
		e.setBuf(e.stash)
		return
	}
	e.setBuf(e.rl.History.At(e.histIdx))
}

func (e *editor) updateSuggestions() {
	e.sug = nil
	if e.rl.Suggest == nil || e.rl.NoSuggestions || e.searching {
		return
	}
	e.sug = e.rl.Suggest(string(e.buf))
}

func (e *editor) complete() {
	if e.rl.Completer == nil || e.searching {
		return
	}
	line := string(e.buf)
	cands := e.rl.Completer.Complete(line)
	e.matches = cands
	if len(cands) == 0 {
		e.sug = nil
		e.redraw()
		return
	}
	if len(cands) == 1 {
		e.setBuf(cands[0])
		e.sug = nil
		e.updateSuggestions()
		e.redraw()
		return
	}
	if prefix := longestCommonPrefix(cands); len([]rune(prefix)) > len(e.buf) {
		e.setBuf(prefix)
	}
	e.matchAt++
	e.sug = cands
	e.redraw()
}

// longestCommonPrefix returns the shared leading text of all candidates.
func longestCommonPrefix(items []string) string {
	if len(items) == 0 {
		return ""
	}
	prefix := []rune(items[0])
	for _, it := range items[1:] {
		r := []rune(it)
		n := 0
		for n < len(prefix) && n < len(r) && prefix[n] == r[n] {
			n++
		}
		prefix = prefix[:n]
	}
	return string(prefix)
}

func (e *editor) startSearch() {
	if e.rl.History == nil {
		return
	}
	e.searching = true
	e.search = ""
	e.saved = append([]rune(nil), e.buf...)
	e.savedPos = e.pos
	e.redraw()
}

func (e *editor) applySearch() {
	if e.rl.History == nil {
		return
	}
	matches := e.rl.History.Search(e.search)
	e.sug = matches
	if len(matches) > 0 {
		e.setBuf(matches[0])
	}
}

func (e *editor) endSearch(keep bool) {
	if !e.searching {
		return
	}
	e.searching = false
	if !keep {
		e.buf = append([]rune(nil), e.saved...)
		e.pos = e.savedPos
	}
	e.sug = nil
}

// NeedsContinuation reports whether a line ends in a way that implies the user
// wants to keep typing (open bracket, trailing operator, dangling quote).
func NeedsContinuation(line string) bool {
	if line == "" {
		return false
	}
	depth := 0
	var quote rune
	inComment := false
	for i, r := range line {
		switch {
		case inComment:
			continue
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '#' && i == 0:
			inComment = true
		case r == '"' || r == '\'' || r == '`':
			quote = r
		case r == '(' || r == '[' || r == '{':
			depth++
		case r == ')' || r == ']' || r == '}':
			depth--
		}
	}
	if quote != 0 || depth > 0 {
		return true
	}
	trimmed := strings.TrimRight(line, " \t")
	if strings.HasSuffix(trimmed, "\\") {
		return true
	}
	if strings.HasSuffix(trimmed, ",") || strings.HasSuffix(trimmed, "&&") ||
		strings.HasSuffix(trimmed, "||") || strings.HasSuffix(trimmed, "+") {
		return true
	}
	return false
}
