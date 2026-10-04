package tui

import (
	"bytes"
	"strings"
	"testing"
)

func newTestScreen(w, h int) (*Screen, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	return NewScreen(buf, Size{Width: w, Height: h}), buf
}

func TestScreenStartEntersAlternateScreen(t *testing.T) {
	s, buf := newTestScreen(20, 5)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	if !strings.Contains(got, "\x1b[?1049h") {
		t.Errorf("the alternate screen was not requested: %q", got)
	}
	if !strings.Contains(got, "\x1b[?25l") {
		t.Errorf("the cursor was not hidden: %q", got)
	}
	s.Stop()
	if !strings.Contains(buf.String(), "\x1b[?1049l") {
		t.Errorf("Stop did not leave the alternate screen: %q", buf.String())
	}
}

// TestScreenWritesOnlyChangedCells is the reason this renderer exists: a
// repaint while the user types must not rewrite the whole screen.
func TestScreenWritesOnlyChangedCells(t *testing.T) {
	s, buf := newTestScreen(20, 3)
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	_ = s.Draw([]string{"hola", "mundo", ""}, nil)
	first := buf.Len()
	buf.Reset()

	// Redraw the same frame: nothing should be written but the cursor move.
	if err := s.Draw([]string{"hola", "mundo", ""}, nil); err != nil {
		t.Fatal(err)
	}
	if buf.Len() >= first {
		t.Errorf("an identical frame wrote %d bytes, the first drew %d", buf.Len(), first)
	}

	// Change one character: only that cell should be written.
	buf.Reset()
	if err := s.Draw([]string{"hola", "mundo!", ""}, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "!") {
		t.Errorf("the changed cell was not drawn: %q", out)
	}
	if strings.Contains(out, "hola") || strings.Contains(out, "mundo\n") {
		t.Errorf("unchanged cells were rewritten: %q", out)
	}
}

// TestScreenClearsShrinkingContent proves a line that gets shorter is erased
// rather than leaving stale characters behind.
func TestScreenClearsShrinkingContent(t *testing.T) {
	s, buf := newTestScreen(20, 2)
	_ = s.Start()
	_ = s.Draw([]string{"una linea larga", "x"}, nil)
	buf.Reset()
	_ = s.Draw([]string{"corta", "x"}, nil)
	out := buf.String()
	if !strings.Contains(out, " ") {
		t.Errorf("the leftover characters were not erased: %q", out)
	}
}

// TestScreenDropsLinesAboveTheTop proves the view scrolls instead of growing
// without bound.
func TestScreenDropsLinesAboveTheTop(t *testing.T) {
	s, _ := newTestScreen(10, 2)
	_ = s.Start()
	lines := []string{"one", "two", "three", "four"}
	_ = s.Draw(lines, nil)
	// Height 2 out of 4 lines shows the two newest ones.
	first := string(s.prev[0].Rune) + string(s.prev[1].Rune) + string(s.prev[2].Rune) + string(s.prev[3].Rune)
	if first != "thre" {
		t.Errorf("the first visible row should start the third line, got %q", first)
	}
}

func TestScreenSetSizeForcesRepaint(t *testing.T) {
	s, buf := newTestScreen(20, 3)
	_ = s.Start()
	_ = s.Draw([]string{"a", "b", "c"}, nil)
	buf.Reset()
	s.SetSize(Size{Width: 30, Height: 5})
	if s.Size() != (Size{Width: 30, Height: 5}) {
		t.Fatalf("the size was not applied: %+v", s.Size())
	}
	// After a resize everything is considered dirty, so a full frame goes out.
	if err := s.Draw([]string{"a", "b", "c"}, nil); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("nothing was drawn after a resize")
	}
}

func TestScreenSetSizeIgnoresNonsense(t *testing.T) {
	s, _ := newTestScreen(20, 3)
	s.SetSize(Size{Width: 0, Height: -1})
	if s.Size() != (Size{Width: 20, Height: 3}) {
		t.Fatalf("an invalid size was accepted: %+v", s.Size())
	}
}

func TestRuneWidth(t *testing.T) {
	cases := map[rune]int{
		'a':  1,
		'á':  1,
		'世':  2,
		'\n': 0,
		0:    0,
	}
	for r, want := range cases {
		if got := runeWidth(r); got != want {
			t.Errorf("runeWidth(%q) = %d, want %d", r, got, want)
		}
	}
}

func TestStyleSGRResetsBeforeAttributes(t *testing.T) {
	got := Style{Fg: Red, Bold: true}.sgr()
	if !strings.HasPrefix(got, "\x1b[0") {
		t.Errorf("a style must start with a reset, got %q", got)
	}
	if !strings.Contains(got, ";1") {
		t.Errorf("bold was not emitted: %q", got)
	}
	if (Style{}).sgr() != "\x1b[0m" {
		t.Errorf("the empty style must be a plain reset, got %q", (Style{}).sgr())
	}
}

func TestTrueColourEncoding(t *testing.T) {
	got := Style{Fg: RGB(0x11, 0x22, 0x33)}.sgr()
	if !strings.Contains(got, "38;2;17;34;51") {
		t.Errorf("true colour was not encoded as expected: %q", got)
	}
}
