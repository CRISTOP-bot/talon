package tui

import (
	"strings"
	"unicode"
)

// Style is a terminal attribute set: a foreground colour, an optional bold or
// dim attribute, and a background colour.
type Style struct {
	Fg     Color
	Bg     Color
	Bold   bool
	Dim    bool
	Italic bool
	// valid distinguishes "no style" from "explicitly reset", which matters
	// when diffing frames.
	valid bool
}

// Color is an ANSI colour index or a true-colour value.
type Color int

// NoColor means "leave this attribute alone". It is declared outside the block
// below on purpose: inside a const block iota counts every line, so having it
// first would shift every named colour by one.
const NoColor Color = -1

// Named colours. Values above 15 are 256-colour indices; true colour is encoded
// as 0x1000000 | 0xRRGGBB.
const (
	// Standard 16 colours.
	Black Color = iota
	Red
	Green
	Yellow
	Blue
	Magenta
	Cyan
	White
	BrightBlack
	BrightRed
	BrightGreen
	BrightYellow
	BrightBlue
	BrightMagenta
	BrightCyan
	BrightWhite
)

// RGB builds a true-colour Style colour.
func RGB(r, g, b uint8) Color { return Color(0x1000000 | int(r)<<16 | int(g)<<8 | int(b)) }

// boldMarker marks a true-colour value so sgr can tell it apart.
const trueColorFlag = 0x1000000

// sgr renders the escape sequence that activates the style. A style is always
// written as a full reset followed by its attributes: it costs a few more bytes
// than a minimal sequence, but a partially reset cell is the classic source of
// "the colour leaks into the next line" bugs.
func (s Style) sgr() string {
	if s == (Style{}) {
		return "\x1b[0m"
	}
	var b strings.Builder
	b.WriteString("\x1b[0")
	if s.Bold {
		b.WriteString(";1")
	}
	if s.Dim {
		b.WriteString(";2")
	}
	if s.Italic {
		b.WriteString(";3")
	}
	writeColor(&b, s.Fg, false)
	writeColor(&b, s.Bg, true)
	b.WriteString("m")
	return b.String()
}

// writeColor appends one colour parameter.
//
// Standard colours are SGR 30-37 for the foreground and 40-47 for the
// background; the bright half is 90-97 and 100-107. A black background is
// treated as "leave it alone", because Black is the zero value of Style and
// honouring it would paint every cell of every frame.
func writeColor(b *strings.Builder, c Color, background bool) {
	if c == NoColor {
		return
	}
	if background && c == Black {
		return
	}
	b.WriteByte(';')
	if int(c)&trueColorFlag != 0 {
		v := int(c) &^ trueColorFlag
		if background {
			b.WriteString("48;2;")
		} else {
			b.WriteString("38;2;")
		}
		b.WriteString(itoa((v >> 16) & 0xff))
		b.WriteByte(';')
		b.WriteString(itoa((v >> 8) & 0xff))
		b.WriteByte(';')
		b.WriteString(itoa(v & 0xff))
		return
	}
	bright := int(c) >= int(BrightBlack)
	n := int(c)
	if bright {
		n -= int(BrightBlack)
	}
	switch {
	case background && bright:
		b.WriteString(itoa(100 + n))
	case background:
		b.WriteString(itoa(40 + n))
	case bright:
		b.WriteString(itoa(90 + n))
	default:
		b.WriteString(itoa(30 + n))
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [12]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// Plain is the default style.
var Plain = Style{}

// Palette is the set of styles a view uses, so the look lives in one place.
type Palette struct {
	Text      Style
	Muted     Style
	Accent    Style
	Success   Style
	Warning   Style
	Danger    Style
	Code      Style
	Border    Style
	Selection Style
	User      Style
	Assistant Style
	Tool      Style
}

// DarkPalette is the default look: a calm, low-contrast interface with one
// accent colour, similar in spirit to opencode's default theme.
func DarkPalette() Palette {
	text := Style{Fg: BrightWhite}
	return Palette{
		Text:      text,
		Muted:     Style{Fg: BrightBlack},
		Accent:    Style{Fg: Blue, Bold: true},
		Success:   Style{Fg: Green},
		Warning:   Style{Fg: Yellow},
		Danger:    Style{Fg: Red},
		Code:      Style{Fg: Cyan},
		Border:    Style{Fg: BrightBlack},
		Selection: Style{Fg: Black, Bg: Blue},
		User:      Style{Fg: BrightBlue, Bold: true},
		Assistant: Style{Fg: BrightWhite},
		Tool:      Style{Fg: Magenta},
	}
}

// LightPalette adapts the look to a bright terminal background.
func LightPalette() Palette {
	p := DarkPalette()
	p.Text = Style{Fg: Black}
	p.Muted = Style{Fg: BrightBlack}
	p.Assistant = Style{Fg: Black}
	p.User = Style{Fg: Blue, Bold: true}
	p.Selection = Style{Fg: BrightWhite, Bg: Blue}
	return p
}

// isCombining reports whether a rune is a zero-width combining mark.
func isCombining(r rune) bool {
	return unicode.In(r, unicode.Mn, unicode.Me, unicode.Cf) && r != 0x00ad
}

// isWide reports whether a rune occupies two terminal columns.
func isWide(r rune) bool {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK radicals, Kangxi, punctuation
		r >= 0x3041 && r <= 0x33FF, // Hiragana, Katakana, Hangul compat, CJK compat
		r >= 0x3400 && r <= 0x4DBF, // CJK ext A
		r >= 0x4E00 && r <= 0x9FFF, // CJK unified
		r >= 0xA000 && r <= 0xA4CF, // Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK compat ideographs
		r >= 0xFF00 && r <= 0xFF60, // Fullwidth forms
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F, // emoji
		r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x20000 && r <= 0x3FFFD:
		return true
	}
	return false
}
