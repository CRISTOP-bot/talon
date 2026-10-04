package tui

import "testing"

// TestNamedColoursUseTheANSINumbers guards a mistake that is easy to make and
// invisible in a diff: an iota offset shifts every colour, so "accent blue"
// renders as magenta.
func TestNamedColoursUseTheANSINumbers(t *testing.T) {
	// Black is the zero value, so an explicit black foreground is a reset; it is
	// listed here as such.
	cases := map[Color]int{
		Red: 31, Green: 32, Yellow: 33,
		Blue: 34, Magenta: 35, Cyan: 36, White: 37,
		BrightBlack: 90, BrightRed: 91, BrightGreen: 92, BrightYellow: 93,
		BrightBlue: 94, BrightMagenta: 95, BrightCyan: 96, BrightWhite: 97,
	}
	for c, want := range cases {
		got := Style{Fg: c}.sgr()
		wantSeq := ";" + itoa(want) + "m"
		if !contains(got, wantSeq) {
			t.Errorf("colour %d should emit %q, got %q", c, wantSeq, got)
		}
	}
}

func TestBlackForegroundIsAReset(t *testing.T) {
	if got := (Style{Fg: Black}).sgr(); got != "\x1b[0m" {
		t.Errorf("an explicit black foreground should be a plain reset, got %q", got)
	}
}

func TestBackgroundUsesTheBackgroundRange(t *testing.T) {
	got := Style{Bg: Red}.sgr()
	if !contains(got, ";41m") {
		t.Errorf("a red background should be SGR 41, got %q", got)
	}
	bright := Style{Bg: BrightBlue}.sgr()
	if !contains(bright, ";104m") {
		t.Errorf("a bright blue background should be SGR 104, got %q", bright)
	}
}

func TestTrueColourBackground(t *testing.T) {
	c := RGB(0x00, 0xFF, 0x00)
	got := Style{Bg: c}.sgr()
	if !contains(got, "48;2;0;255;0") {
		t.Errorf("true colour background was not encoded: %q", got)
	}
}

func TestNoColorEmitsNothing(t *testing.T) {
	plain := Style{Fg: NoColor, Bold: true}
	if got := plain.sgr(); contains(got, "39") || contains(got, "49") {
		t.Errorf("NoColor must not emit an SGR parameter, got %q", got)
	}
	if got := (Style{}).sgr(); got != "\x1b[0m" {
		t.Errorf("the empty style must be a plain reset, got %q", got)
	}
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
