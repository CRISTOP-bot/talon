package term

import (
	"fmt"
	"io"
	"strings"
	"unicode"
)

// ANSI control sequences used by the editor renderer.
const (
	seqEraseLine   = "\x1b[K"
	seqEraseBelow  = "\x1b[J"
	seqCursorUp    = "\x1b[%dA"
	seqCursorLeft  = "\x1b[%dD"
	seqCursorRight = "\x1b[%dC"
)

// Prompt holds the styling of the two prompt markers.
type Prompt struct {
	// Marker is the leading glyph, e.g. "❯" or "$".
	Marker string
	// MarkerStyle is an SGR sequence applied to the marker.
	MarkerStyle string
	// Hint is the text shown after the marker, e.g. "ask".
	Hint string
	// HintStyle is an SGR sequence applied to the hint.
	HintStyle string
}

// DefaultAskPrompt is the interactive prompt.
func DefaultAskPrompt() Prompt {
	return Prompt{Marker: "❯", MarkerStyle: "\x1b[1;38;5;39m", Hint: "", HintStyle: ""}
}

// DefaultShellPrompt is used when the agent is running a command.
func DefaultShellPrompt() Prompt {
	return Prompt{Marker: "$", MarkerStyle: "\x1b[38;5;39m"}
}

// paint wraps text in an SGR sequence, tolerating empty styles.
func paint(style, text string) string {
	if style == "" || text == "" {
		return text
	}
	return style + text + "\x1b[0m"
}

// Render produces the styled prompt string.
func (p Prompt) Render() string {
	out := paint(p.MarkerStyle, p.Marker)
	if p.Hint != "" {
		hint := " " + p.Hint
		out += paint(p.HintStyle, hint)
	}
	return out + " "
}

// visibleWidth counts terminal columns, ignoring escape sequences and treating
// wide runes as two columns.
func smaller(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func visibleWidth(s string) int {
	w := 0
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		case unicode.Is(unicode.Mn, r):
			// combining mark, no width
		case isWide(r):
			w += 2
		default:
			w++
		}
	}
	return w
}

func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6:
		return true
	}
	return false
}

// width returns the terminal width, falling back to 80 columns.
func (r *Readline) width() int {
	w, _, err := Size(r.In)
	if err != nil || w <= 0 {
		return 80
	}
	return w
}

// redraw repaints the whole input block: the prompt, every buffer line and the
// hint lines, then parks the cursor at the right position.
func (e *editor) redraw() {
	out := e.rl.Out
	prompt := e.promptText()
	promptW := visibleWidth(prompt)

	// Split the buffer into physical lines.
	lines := splitLines(string(e.buf))
	var outLines []string
	for i, ln := range lines {
		prefix := ""
		if i == 0 {
			prefix = prompt
		}
		outLines = append(outLines, prefix+ln)
	}
	for _, h := range e.hintLines(promptW) {
		outLines = append(outLines, h)
	}

	var b strings.Builder
	// Erase what the previous render drew.
	if e.rl.last > 0 {
		if e.rl.last > 1 {
			fmt.Fprintf(&b, seqCursorUp, e.rl.last-1)
		}
		b.WriteString("\r")
	}
	for i, ln := range outLines {
		if i > 0 {
			b.WriteString("\r\n")
		}
		// Truncate horizontally; the editor scrolls instead of wrapping.
		b.WriteString(ln)
		b.WriteString(seqEraseLine)
	}
	// Anything below the block is stale.
	b.WriteString("\r\n" + seqEraseBelow)

	// Position the cursor inside the block.
	cursorLine, cursorCol := e.cursorPosition(promptW, lines)
	fromBottom := len(outLines) - cursorLine - 1
	if fromBottom > 0 {
		fmt.Fprintf(&b, seqCursorUp, fromBottom)
	}
	if cursorCol > 0 {
		fmt.Fprintf(&b, seqCursorRight, cursorCol)
	}
	io.WriteString(out, b.String())
	e.rl.last = len(outLines)
}

func (e *editor) promptText() string {
	if e.rl.Prompt != (Prompt{}) {
		return e.rl.Prompt.Render()
	}
	return DefaultAskPrompt().Render()
}

func (e *editor) cursorPosition(promptW int, lines []string) (line, col int) {
	before := string(e.buf[:min(e.pos, len(e.buf))])
	parts := splitLines(before)
	line = len(parts) - 1
	last := parts[len(parts)-1]
	if line == 0 {
		col = promptW + visibleWidth(last)
	} else {
		col = visibleWidth(last)
	}
	return line, col
}

// hintLines renders suggestion and completion lists.
func (e *editor) hintLines(promptW int) []string {
	if len(e.sug) == 0 {
		return nil
	}
	items := e.sug
	if len(items) > 8 {
		items = items[:8]
	}
	trimmed := make([]string, 0, len(items))
	for _, it := range items {
		it = strings.ReplaceAll(it, "\n", " ⏎ ")
		avail := e.rl.width() - promptW - 2
		if len(it) > avail {
			if avail > 3 {
				it = it[:avail-1] + "…"
			} else {
				continue
			}
		}
		trimmed = append(trimmed, "  "+it)
	}
	return trimmed
}

// clear erases the block drawn by the last render, leaving the cursor at the
// start of a fresh line.
func (e *editor) clear() {
	if e.rl.last <= 0 {
		return
	}
	var b strings.Builder
	if e.rl.last > 1 {
		fmt.Fprintf(&b, seqCursorUp, e.rl.last-1)
	}
	b.WriteString("\r" + seqEraseBelow)
	io.WriteString(e.rl.Out, b.String())
	e.rl.last = 0
}

// splitLines splits text on newlines, keeping at least one element.
func splitLines(s string) []string {
	if s == "" {
		return []string{""}
	}
	parts := strings.Split(s, "\n")
	if len(parts) == 0 {
		return []string{""}
	}
	return parts
}
