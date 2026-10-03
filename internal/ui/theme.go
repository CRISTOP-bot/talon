// Package ui renders everything the user sees: status lines, panels, tables,
// markdown, syntax-highlighted code and diffs.
package ui

import (
	"os"
	"strings"
)

// Theme holds the ANSI sequences used for each semantic style. An empty field
// means "no styling", which is what the mono theme provides.
type Theme struct {
	Name      string
	Reset     string
	Bold      string
	Dim       string
	Italic    string
	Primary   string
	Accent    string
	Success   string
	Warning   string
	Error     string
	Muted     string
	Code      string
	CodeBG    string
	Heading   string
	Link      string
	DiffAdd   string
	DiffDel   string
	DiffHunk  string
	DiffMeta  string
	Border    string
	Selection string
}

// DefaultTheme is the palette used on dark terminals.
func DefaultTheme() Theme {
	return Theme{
		Name:      "default",
		Reset:     "\x1b[0m",
		Bold:      "\x1b[1m",
		Dim:       "\x1b[2m",
		Italic:    "\x1b[3m",
		Primary:   "\x1b[38;5;39m",
		Accent:    "\x1b[38;5;170m",
		Success:   "\x1b[38;5;42m",
		Warning:   "\x1b[38;5;214m",
		Error:     "\x1b[38;5;203m",
		Muted:     "\x1b[38;5;244m",
		Code:      "\x1b[38;5;252m",
		CodeBG:    "\x1b[48;5;236m",
		Heading:   "\x1b[1;38;5;39m",
		Link:      "\x1b[4;38;5;39m",
		DiffAdd:   "\x1b[38;5;42m",
		DiffDel:   "\x1b[38;5;203m",
		DiffHunk:  "\x1b[38;5;170m",
		DiffMeta:  "\x1b[38;5;244m",
		Border:    "\x1b[38;5;240m",
		Selection: "\x1b[48;5;238m",
	}
}

// MonoTheme disables all styling, for dumb terminals and golden tests.
func MonoTheme() Theme {
	return Theme{Name: "mono"}
}

// ThemeByName resolves a theme name.
func ThemeByName(name string) Theme {
	switch strings.ToLower(name) {
	case "", "default", "dark":
		return DefaultTheme()
	case "mono", "plain", "none":
		return MonoTheme()
	default:
		return DefaultTheme()
	}
}

// NoColor reports whether the theme applies no styling at all.
func (t Theme) NoColor() bool { return t.Primary == "" && t.Bold == "" }

// Paint wraps text in a style, returning it unchanged when style is empty.
func (t Theme) Paint(style, text string) string {
	if style == "" || text == "" {
		return text
	}
	return style + text + t.Reset
}

// Primary, Muted, Bold, and friends are convenience wrappers.
func (t Theme) S(s string) string          { return t.Paint(t.Primary, s) }
func (t Theme) Ok(s string) string         { return t.Paint(t.Success, s) }
func (t Theme) Warn(s string) string       { return t.Paint(t.Warning, s) }
func (t Theme) Err(s string) string        { return t.Paint(t.Error, s) }
func (t Theme) Faint(s string) string      { return t.Paint(t.Muted, s) }
func (t Theme) Head(s string) string       { return t.Paint(t.Heading, s) }
func (t Theme) Strong(s string) string     { return t.Paint(t.Bold, s) }
func (t Theme) Emph(s string) string       { return t.Paint(t.Italic, s) }
func (t Theme) BorderLine(s string) string { return t.Paint(t.Border, s) }

// SupportsColor detects whether stdout is a color-capable terminal.
func SupportsColor(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	return true
}
