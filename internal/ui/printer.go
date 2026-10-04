package ui

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// Glyphs used for status lines. They degrade to ASCII when styling is off.
const (
	GlyphBullet  = "•"
	GlyphOK      = "✓"
	GlyphFail    = "✗"
	GlyphWarn    = "!"
	GlyphInfo    = "i"
	GlyphStep    = "◐"
	GlyphDone    = "✔"
	GlyphArrow   = "❯"
	GlyphBullet2 = "·"
)

// Printer writes structured, styled output.
type Printer struct {
	Out   io.Writer
	Err   io.Writer
	Theme Theme
	// Width is the wrap width; 0 disables wrapping.
	Width int
	// Quiet suppresses Info/Muted lines.
	Quiet bool
}

// NewPrinter builds a printer with the given theme.
// SetOut redirects the printer. The full-screen interface uses it to send
// command output into the conversation instead of the terminal.
func (p *Printer) SetOut(out, errOut io.Writer) {
	p.Out = out
	if errOut != nil {
		p.Err = errOut
	}
}

func NewPrinter(out, errOut io.Writer, theme Theme) *Printer {
	return &Printer{Out: out, Err: errOut, Theme: theme, Width: 100}
}

// Printf writes raw formatted text to stdout.
func (p *Printer) Printf(format string, args ...any) {
	fmt.Fprintf(p.Out, format, args...)
}

// Line writes a raw line to stdout.
func (p *Printer) Line(s string) { fmt.Fprintln(p.Out, s) }

// Status prints an aligned status line: a glyph, a label and a message.
func (p *Printer) Status(glyph, style, label, msg string) {
	var b strings.Builder
	if glyph != "" {
		b.WriteString(p.Theme.Paint(style, glyph))
		b.WriteString(" ")
	}
	if label != "" {
		b.WriteString(p.Theme.Strong(label))
		if msg != "" {
			b.WriteString(" ")
		}
	}
	b.WriteString(msg)
	fmt.Fprintln(p.Out, b.String())
}

// Info prints an informational line.
func (p *Printer) Info(msg string) { p.Status(GlyphBullet, p.Theme.Primary, "", msg) }

// Step prints a work-in-progress line.
func (p *Printer) Step(msg string) { p.Status(GlyphStep, p.Theme.Primary, "", msg) }

// Success prints a success line.
func (p *Printer) Success(msg string) { p.Status(GlyphOK, p.Theme.Success, "", msg) }

// Failure prints a failure line.
func (p *Printer) Failure(msg string) { p.Status(GlyphFail, p.Theme.Error, "", msg) }

// Warn prints a warning line.
func (p *Printer) Warn(msg string) { p.Status(GlyphWarn, p.Theme.Warning, "", msg) }

// Detail prints a muted continuation line.
func (p *Printer) Detail(msg string) {
	if p.Quiet {
		return
	}
	for _, line := range strings.Split(msg, "\n") {
		fmt.Fprintf(p.Out, "  %s\n", p.Theme.Faint(line))
	}
}

// Muted prints a muted line, unless quiet mode is on.
func (p *Printer) Muted(msg string) {
	if p.Quiet {
		return
	}
	fmt.Fprintln(p.Out, p.Theme.Faint(msg))
}

// Blank prints an empty line.
func (p *Printer) Blank() { fmt.Fprintln(p.Out) }

// Rule prints a horizontal separator sized to the terminal width.
func (p *Printer) Rule() {
	w := p.Width
	if w <= 0 || w > 100 {
		w = 80
	}
	fmt.Fprintln(p.Out, p.Theme.BorderLine(strings.Repeat("─", w)))
}

// Panel draws a titled box around the given lines.
func (p *Printer) Panel(title string, lines []string) {
	width := p.panelWidth(lines)
	if width < 12 {
		width = 12
	}
	if p.Width > 0 && width > p.Width-4 {
		width = p.Width - 4
	}
	t := p.Theme
	var b strings.Builder
	head := "╭─"
	if t.NoColor() {
		head = "+-"
	}
	b.WriteString(t.BorderLine(head))
	if title != "" {
		b.WriteString(t.BorderLine(" "))
		b.WriteString(t.Strong(title))
		b.WriteString(t.BorderLine(" "))
	}
	pad := width - utf8.RuneCountInString(title) - 1
	if pad < 0 {
		pad = 0
	}
	b.WriteString(t.BorderLine(strings.Repeat("─", pad)))
	mid, bot := "│", "╰"
	if t.NoColor() {
		mid, bot = "|", "+"
	}
	b.WriteString(mid + "\n")
	for _, line := range lines {
		content := p.Theme.Code + line + t.Reset
		pad := width - utf8.RuneCountInString(stripANSI(line))
		if pad < 0 {
			pad = 0
		}
		b.WriteString(t.BorderLine(mid) + " ")
		b.WriteString(content)
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(t.BorderLine(mid) + "\n")
	}
	b.WriteString(t.BorderLine(bot))
	b.WriteString(t.BorderLine(strings.Repeat("─", width)))
	fmt.Fprint(p.Out, b.String())
}

func (p *Printer) panelWidth(lines []string) int {
	w := 0
	for _, l := range lines {
		if n := utf8.RuneCountInString(stripANSI(l)); n > w {
			w = n
		}
	}
	return w
}

// Table renders aligned columns. Headers are styled; rows are padded.
func (p *Printer) Table(headers []string, rows [][]string) {
	widths := make([]int, len(headers))
	for i, h := range headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) {
				if n := utf8.RuneCountInString(stripANSI(cell)); n > widths[i] {
					widths[i] = n
				}
			}
		}
	}
	var b strings.Builder
	writeRow := func(cells []string, style string) {
		parts := make([]string, 0, len(cells))
		for i, c := range cells {
			pad := 0
			if i < len(widths) {
				pad = widths[i] - utf8.RuneCountInString(stripANSI(c))
			}
			if pad < 0 {
				pad = 0
			}
			parts = append(parts, p.Theme.Paint(style, c)+strings.Repeat(" ", pad))
		}
		b.WriteString("  " + strings.TrimRight(strings.Join(parts, "  "), " ") + "\n")
	}
	writeRow(headers, p.Theme.Muted)
	seps := make([]string, len(headers))
	for i, h := range headers {
		seps[i] = strings.Repeat("─", max(3, utf8.RuneCountInString(h)))
	}
	writeRow(seps, p.Theme.Border)
	for _, row := range rows {
		writeRow(row, "")
	}
	fmt.Fprint(p.Out, b.String())
}

// KV prints aligned key/value lines.
func (p *Printer) KV(pairs [][2]string) {
	width := 0
	for _, kv := range pairs {
		if n := utf8.RuneCountInString(kv[0]); n > width {
			width = n
		}
	}
	var b strings.Builder
	for _, kv := range pairs {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, p.Theme.Faint(kv[0]), kv[1])
	}
	fmt.Fprint(p.Out, b.String())
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// WrapText hard-wraps text at the printer width, preserving explicit newlines.
func (p *Printer) WrapText(s string) string {
	width := p.Width
	if width <= 0 {
		width = 100
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if utf8.RuneCountInString(para) <= width {
			out = append(out, para)
			continue
		}
		var cur string
		for _, word := range strings.Fields(para) {
			switch {
			case cur == "":
				cur = word
			case utf8.RuneCountInString(cur)+1+utf8.RuneCountInString(word) <= width:
				cur += " " + word
			default:
				out = append(out, cur)
				cur = word
			}
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	return strings.Join(out, "\n")
}

// stripANSI removes SGR sequences, used for width calculations.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case inEsc:
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
				inEsc = false
			}
		case r == 0x1b:
			inEsc = true
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// StripANSI is the exported form of stripANSI.
func StripANSI(s string) string { return stripANSI(s) }

// DisplayWidth returns the visible column count of s.
func DisplayWidth(s string) int {
	return utf8.RuneCountInString(stripANSI(s))
}
