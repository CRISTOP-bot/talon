package tui

import "strings"

// Span is a run of text with one style. A view is built as spans, then flattened
// into the per-cell styles the screen needs.
type Span struct {
	Text  string
	Style Style
}

// Line is a row of spans.
type Line []Span

// Text builds a line from plain text in one style.
func Text(style Style, text string) Line {
	return Line{{Text: text, Style: style}}
}

// String flattens a line, dropping styling.
func (l Line) String() string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.Text)
	}
	return b.String()
}

// width returns the printable width of the line.
func (l Line) width() int {
	w := 0
	for _, s := range l {
		for _, r := range s.Text {
			w += runeWidth(r)
		}
	}
	return w
}

// padTo appends spaces so the line occupies at least n columns.
func (l Line) padTo(n int, style Style) Line {
	if l.width() >= n {
		return l
	}
	return append(l, Span{Text: strings.Repeat(" ", n-l.width()), Style: style})
}

// Layout is the opencode-like frame: a compact header, the conversation, and a
// footer that holds the editor plus the mode indicator.
type Layout struct {
	// Header lines at the top.
	Header []Line
	// FooterHint is the single line of hints under the editor.
	FooterHint Line
	// Body is the scrolling conversation.
	Body []Line
	// Editor is the prompt, possibly several rows tall.
	Editor []Line
	// Overlay replaces the body area when it is not empty.
	Overlay []Line
	// Mode is shown in the lower right corner, as in opencode.
	Mode string
	// Status is an optional transient message next to the editor.
	Status string
	// StatusStyle colours Status.
	StatusStyle Style
}

// Render flattens a layout into screen lines and per-cell styles.
func Render(p Palette, l Layout, width, height int) (lines []string, styles [][]Style, cursorX, cursorY int) {
	rows := make([][]Style, 0, height)
	out := make([]string, 0, height)
	put := func(l Line) {
		if len(rows) >= height {
			return
		}
		text, cells := flatten(l, width)
		out = append(out, text)
		rows = append(rows, cells)
	}

	// Header: a single line, dimmed, with a rule under it.
	head := Line{}
	for _, h := range l.Header {
		head = append(head, h...)
	}
	put(head)
	put(Line{{Text: strings.Repeat("─", width), Style: p.Border}})

	footer := make([]Line, 0, len(l.Editor)+2)
	footer = append(footer, l.Editor...)
	if l.FooterHint.String() != "" {
		footer = append(footer, l.FooterHint)
	}
	// The status line carries the mode indicator on the right, as opencode does.
	status := Line{}
	if l.Status != "" {
		status = append(status, Span{Text: " " + l.Status, Style: l.StatusStyle})
	}
	if l.Mode != "" {
		gap := width - l.EditorWidth() - status.width() - runeWidthIn(l.Mode)
		if gap < 1 {
			gap = 1
		}
		status = append(status, Span{Text: strings.Repeat(" ", gap), Style: p.Muted})
		status = append(status, Span{Text: l.Mode, Style: p.Accent})
	}
	footer = append(footer, status)

	footerRows := len(footer)
	bodyHeight := height - 1 - 1 - footerRows - 1 // header, rule, footer, blank
	if bodyHeight < 1 {
		bodyHeight = 1
	}

	// Body: the last visible rows of the conversation, so it scrolls naturally.
	body := l.Body
	if l.Overlay != nil {
		body = l.Overlay
	}
	if len(body) > bodyHeight {
		body = body[len(body)-bodyHeight:]
	}
	blankRows := bodyHeight - len(body)
	for i := 0; i < blankRows; i++ {
		put(nil)
	}
	for _, line := range body {
		put(line)
	}

	for _, line := range footer {
		put(line)
	}
	// Pad to the full height so stale cells are overwritten.
	for len(out) < height {
		put(nil)
	}

	// The caret goes at the end of the first editor row.
	if len(l.Editor) > 0 {
		editorTop := height - footerRows
		cursorY = editorTop
		cursorX = l.EditorWidth()
	}
	return out, rows, cursorX, cursorY
}

// EditorWidth is the printable width of the first editor row, which is where the
// caret belongs.
func (l *Layout) EditorWidth() int {
	if len(l.Editor) == 0 {
		return 0
	}
	return l.Editor[0].width()
}

func runeWidthIn(s string) int {
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// flatten converts spans into a plain string plus one style per cell, padded to
// the requested width.
func flatten(l Line, width int) (string, []Style) {
	var b strings.Builder
	cells := make([]Style, 0, width)
	col := 0
	for _, span := range l {
		for _, r := range span.Text {
			w := runeWidth(r)
			if col+w > width {
				return b.String(), cells
			}
			b.WriteRune(r)
			cells = append(cells, span.Style)
			col++
			if w == 2 {
				cells = append(cells, span.Style)
				col++
			}
		}
	}
	for col < width {
		b.WriteByte(' ')
		cells = append(cells, Style{})
		col++
	}
	return b.String(), cells
}

// Rule returns a horizontal rule line.
func Rule(p Palette, width int) Line {
	return Line{{Text: strings.Repeat("─", width), Style: p.Border}}
}

// HeaderLine builds the top bar: the binary name and version on the left, and
// context on the right.
func HeaderLine(p Palette, width int, app string, version string, context string) Line {
	left := Span{Text: app, Style: Style{Fg: BrightCyan, Bold: true}}
	if version != "" {
		left.Text += " " + version
		left.Style = p.Accent
	}
	line := Line{left}
	if context != "" {
		gap := width - line.width() - runeWidthIn(context)
		if gap < 1 {
			gap = 1
		}
		line = append(line, Span{Text: strings.Repeat(" ", gap), Style: p.Muted})
		line = append(line, Span{Text: context, Style: p.Muted})
	}
	return line
}

// ModeLabel renders the lower-right mode indicator.
func ModeLabel(p Palette, plan bool) string {
	if plan {
		return "plan"
	}
	return "build"
}

// Box draws a bordered panel, used by the overlays.
func Box(p Palette, width int, title string, body []Line) []Line {
	inner := width - 2
	lines := make([]Line, 0, len(body)+2)
	var top strings.Builder
	top.WriteString("┌")
	if title != "" {
		top.WriteString(" " + title + " ")
	}
	for i := runeWidthIn(top.String()); i < inner; i++ {
		top.WriteString("─")
	}
	top.WriteString("┐")
	lines = append(lines, Line{{Text: top.String(), Style: p.Border}})
	for _, l := range body {
		row := Line{{Text: "│", Style: p.Border}}
		row = append(row, trimTo(l, inner)...)
		row = append(row, Span{Text: strings.Repeat(" ", max(0, inner-trimTo(l, inner).width())), Style: p.Text})
		row = append(row, Span{Text: "│", Style: p.Border})
		lines = append(lines, row)
	}
	var bottom strings.Builder
	bottom.WriteString("└")
	for i := 0; i < inner; i++ {
		bottom.WriteString("─")
	}
	bottom.WriteString("┘")
	lines = append(lines, Line{{Text: bottom.String(), Style: p.Border}})
	return lines
}

func trimTo(l Line, width int) Line {
	out := make(Line, 0, len(l))
	w := 0
	for _, s := range l {
		for _, r := range s.Text {
			rw := runeWidth(r)
			if w+rw > width {
				return out
			}
			w += rw
		}
		out = append(out, s)
	}
	return out
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// WrapText wraps text to a width, preserving explicit newlines.
func WrapText(style Style, text string, width int) []Line {
	var out []Line
	if width < 8 {
		width = 8
	}
	for _, raw := range strings.Split(text, "\n") {
		words := strings.Fields(raw)
		if len(words) == 0 {
			out = append(out, Text(style, ""))
			continue
		}
		cur := ""
		for _, w := range words {
			if cur == "" {
				cur = w
				continue
			}
			if runeWidthIn(cur)+1+runeWidthIn(w) > width {
				out = append(out, Text(style, cur))
				cur = w
				continue
			}
			cur += " " + w
		}
		out = append(out, Text(style, cur))
	}
	return out
}

// Bullet renders a dim bullet, used for tool calls and notices.
func Bullet(p Palette, text string) Line {
	return Line{
		{Text: "  " + glyphDot + " ", Style: p.Muted},
		{Text: text, Style: p.Text},
	}
}

const glyphDot = "•"
