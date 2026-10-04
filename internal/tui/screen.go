// Package tui renders a full-screen terminal interface.
//
// The renderer keeps a cell buffer for what is on screen and a cell buffer for
// what should be there, then writes only the cells that differ. That keeps the
// number of bytes sent to the terminal small, which matters because every byte
// written while the user is typing is latency they feel.
package tui

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/CRISTOP-bot/talon/internal/term"
)

// Cell is one character position on the screen.
type Cell struct {
	Rune  rune
	Style Style
}

// Screen owns the terminal: the alternate screen buffer, the cursor and the
// size. It never reads input; input is handled by the caller.
type Screen struct {
	out    *bufio.Writer
	file   io.Writer
	closer io.Closer
	// prev is what the terminal currently shows.
	prev []Cell
	// cursor is where the hardware cursor should end up.
	cursorX, cursorY int
	width, height    int
	// rawCursor is true when the cursor should be left visible.
	rawCursor bool
	started   bool
	lastSize  Size
}

// Size is a terminal size in cells.
type Size struct {
	Width  int
	Height int
}

// NewScreen prepares a screen. When size is zero the terminal size is queried.
// The screen is not entered until Start is called.
func NewScreen(out io.Writer, size Size) *Screen {
	s := &Screen{out: bufio.NewWriterSize(out, 32*1024), file: out}
	if size.Width == 0 || size.Height == 0 {
		if f, ok := out.(*os.File); ok {
			if w, h, err := term.Size(f); err == nil {
				size.Width, size.Height = w, h
			}
		}
	}
	if size.Width <= 0 {
		size.Width = 80
	}
	if size.Height <= 0 {
		size.Height = 24
	}
	s.width, s.height = size.Width, size.Height
	s.prev = blank(s.width, s.height)
	s.lastSize = size
	return s
}

// Start switches the terminal to the alternate screen, clears it and leaves the
// scrollback untouched, which is what a full-screen interface needs.
func (s *Screen) Start() error {
	if s.started {
		return nil
	}
	s.started = true
	// 1049: alternate screen. 25: hide cursor. 1000/1002/1006: no mouse.
	_, err := io.WriteString(s.file, "\x1b[?1049h\x1b[?25l\x1b[?1000l\x1b[?1002l\x1b[?1006l\x1b[2J\x1b[H")
	return err
}

// Stop restores the terminal. It is safe to call more than once.
func (s *Screen) Stop() {
	if !s.started {
		return
	}
	s.started = false
	// Show the cursor, leave the alternate screen, clear it from view.
	_, _ = io.WriteString(s.file, "\x1b[?25h\x1b[?1049l")
	_ = s.out.Flush()
}

// Flush writes buffered output to the terminal.
func (s *Screen) Flush() error { return s.out.Flush() }

// Size returns the current terminal size.
func (s *Screen) Size() Size { return Size{Width: s.width, Height: s.height} }

// SetSize applies a new terminal size and forces a full repaint.
func (s *Screen) SetSize(size Size) {
	if size.Width <= 0 || size.Height <= 0 {
		return
	}
	if size == s.lastSize {
		return
	}
	s.lastSize = size
	s.width, s.height = size.Width, size.Height
	s.prev = blank(s.width, s.height)
	s.ShowCursor(true)
}

// ShowCursor controls whether the hardware cursor is visible. It does nothing
// before Start, so a view can be rendered in a test without a terminal.
func (s *Screen) ShowCursor(show bool) {
	if !s.started {
		return
	}
	if show == s.rawCursor {
		return
	}
	s.rawCursor = show
	if show {
		_, _ = io.WriteString(s.file, "\x1b[?25h")
	} else {
		_, _ = io.WriteString(s.file, "\x1b[?25l")
	}
}

// SetCursor positions the hardware cursor, in cell coordinates from the top
// left. It is clamped to the screen.
func (s *Screen) SetCursor(x, y int) {
	s.cursorX, s.cursorY = x, y
}

// Cursor returns the cursor position the screen asked for.
func (s *Screen) Cursor() (int, int) { return s.cursorX, s.cursorY }

// Draw paints the frame and flushes it. Lines beyond the screen height are
// dropped; lines are padded or truncated to the width. Trailing blank cells are
// not written, so a mostly empty frame costs almost nothing.
func (s *Screen) Draw(lines []string, styles [][]Style) error {
	if !s.started {
		return nil
	}
	next := blank(s.width, s.height)
	height := len(lines)
	if height > s.height {
		lines = lines[len(lines)-s.height:]
		if styles != nil {
			styles = styles[len(styles)-s.height:]
		}
		height = s.height
	}
	for row := 0; row < height; row++ {
		text := lines[row]
		var rowStyles []Style
		if styles != nil && row < len(styles) {
			rowStyles = styles[row]
		}
		col := 0
		for _, r := range text {
			if col >= s.width {
				break
			}
			st := Style{}
			if col < len(rowStyles) {
				st = rowStyles[col]
			}
			next[row*s.width+col] = Cell{Rune: r, Style: st}
			col++
			// A double-width rune occupies two cells.
			if width := runeWidth(r); width == 2 && col < s.width {
				next[row*s.width+col] = Cell{Rune: 0, Style: st}
				col++
			}
		}
	}
	return s.blit(next)
}

// blit writes the difference between the previous frame and the next one.
// Only changed cells are written, and the cursor is moved only when it is not
// already where the next cell is.
func (s *Screen) blit(next []Cell) error {
	var b strings.Builder
	lastStyle := Style{valid: false}
	cursorRow, cursorCol := -1, -1
	for i, cell := range next {
		if cell == s.prev[i] {
			continue
		}
		row, col := i/s.width, i%s.width
		if row != cursorRow || col != cursorCol {
			fmt.Fprintf(&b, "\x1b[%d;%dH", row+1, col+1)
		}
		if cell.Style != lastStyle {
			b.WriteString(cell.Style.sgr())
			lastStyle = cell.Style
		}
		// A cell masked by a wide rune is written as part of its neighbour.
		if cell.Rune == 0 {
			cursorRow, cursorCol = row, col+1
			continue
		}
		b.WriteRune(cell.Rune)
		cursorRow, cursorCol = row, col+runeWidth(cell.Rune)
	}
	if s.rawCursor {
		x := clamp(s.cursorX, 0, s.width-1) + 1
		y := clamp(s.cursorY, 0, s.height-1) + 1
		fmt.Fprintf(&b, "\x1b[%d;%dH", y, x)
		b.WriteString("\x1b[?25h")
	} else {
		b.WriteString("\x1b[?25l")
	}
	s.prev = next
	if b.Len() == 0 {
		return s.out.Flush()
	}
	if _, err := io.WriteString(s.out, b.String()); err != nil {
		return err
	}
	return s.out.Flush()
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func blank(w, h int) []Cell {
	cells := make([]Cell, w*h)
	for i := range cells {
		cells[i] = Cell{Rune: ' '}
	}
	return cells
}

// runeWidth reports how many columns a rune occupies. Talon only needs the
// common cases: combining marks and East Asian wide characters.
func runeWidth(r rune) int {
	switch {
	case r == 0:
		return 0
	case r < 0x20:
		return 0
	case r == utf8.RuneError:
		return 1
	case isCombining(r):
		return 0
	case isWide(r):
		return 2
	default:
		return 1
	}
}
