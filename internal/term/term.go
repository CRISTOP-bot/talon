// Package term provides terminal capabilities: raw mode, window size and key
// decoding. It is the only package that talks to terminal control codes, so
// the rest of Talon stays portable.
package term

import (
	"errors"
	"os"
	"strings"
	"unicode/utf8"
)

// Key identifies a key press.
type Key int

// Keys produced by Decode.
const (
	KeyRune Key = iota
	KeyEnter
	KeyTab
	KeyBackspace
	KeyDelete
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
	KeyCtrlC
	KeyCtrlD
	KeyCtrlA
	KeyCtrlE
	KeyCtrlK
	KeyCtrlU
	KeyCtrlW
	KeyCtrlL
	KeyCtrlR
	KeyCtrlV
	KeyEsc
	KeyUnknown
)

// Event is a decoded key press. Rune is only valid for KeyRune.
type Event struct {
	Key   Key
	Rune  rune
	Alt   bool
	Shift bool
}

// String renders the event for debugging and tests.
func (e Event) String() string {
	if e.Key == KeyRune {
		if e.Alt {
			return "alt+" + string(e.Rune)
		}
		return string(e.Rune)
	}
	return keyNames[e.Key]
}

var keyNames = map[Key]string{
	KeyEnter: "enter", KeyTab: "tab", KeyBackspace: "backspace", KeyDelete: "delete",
	KeyUp: "up", KeyDown: "down", KeyLeft: "left", KeyRight: "right",
	KeyHome: "home", KeyEnd: "end", KeyPageUp: "pageup", KeyPageDown: "pagedown",
	KeyCtrlC: "ctrl-c", KeyCtrlD: "ctrl-d", KeyCtrlA: "ctrl-a", KeyCtrlE: "ctrl-e",
	KeyCtrlK: "ctrl-k", KeyCtrlU: "ctrl-u", KeyCtrlW: "ctrl-w", KeyCtrlL: "ctrl-l",
	KeyCtrlR: "ctrl-r", KeyCtrlV: "ctrl-v", KeyEsc: "esc", KeyUnknown: "unknown",
}

// ErrClosed is returned by ReadByte when the input stream ends.
var ErrClosed = errors.New("input closed")

// IsTerminal reports whether f is attached to a terminal.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Decode converts a byte (or escape sequence) into an Event. It is exported so
// key handling can be unit-tested without a real terminal.
func Decode(buf []byte) (Event, int, error) {
	if len(buf) == 0 {
		return Event{Key: KeyUnknown}, 0, ErrClosed
	}
	b := buf[0]
	switch b {
	case 0x03:
		return Event{Key: KeyCtrlC}, 1, nil
	case 0x04:
		return Event{Key: KeyCtrlD}, 1, nil
	case 0x01:
		return Event{Key: KeyCtrlA}, 1, nil
	case 0x05:
		return Event{Key: KeyCtrlE}, 1, nil
	case 0x0b:
		return Event{Key: KeyCtrlK}, 1, nil
	case 0x15:
		return Event{Key: KeyCtrlU}, 1, nil
	case 0x17:
		return Event{Key: KeyCtrlW}, 1, nil
	case 0x0c:
		return Event{Key: KeyCtrlL}, 1, nil
	case 0x12:
		return Event{Key: KeyCtrlR}, 1, nil
	case 0x16:
		return Event{Key: KeyCtrlV}, 1, nil
	case '\r', '\n':
		return Event{Key: KeyEnter}, 1, nil
	case '\t':
		return Event{Key: KeyTab}, 1, nil
	case 0x7f, 0x08:
		return Event{Key: KeyBackspace}, 1, nil
	}

	if b == 0x1b {
		if len(buf) < 2 {
			// Lone ESC: treat as the escape key.
			return Event{Key: KeyEsc}, 1, nil
		}
		seq := buf[1:]
		if seq[0] == '[' {
			seq = seq[1:]
		} else if seq[0] == 'O' {
			seq = seq[1:]
		} else {
			// ESC followed by a rune = alt+key.
			r, size := utf8.DecodeRune(seq)
			if r == utf8.RuneError && size <= 1 {
				return Event{Key: KeyEsc}, 1, nil
			}
			return Event{Key: KeyRune, Rune: r, Alt: true}, 1 + size, nil
		}
		if len(seq) == 0 {
			return Event{Key: KeyEsc}, 1, nil
		}
		switch seq[len(seq)-1] {
		case 'A':
			return Event{Key: KeyUp}, 2 + len(seq), nil
		case 'B':
			return Event{Key: KeyDown}, 2 + len(seq), nil
		case 'C':
			return Event{Key: KeyRight}, 2 + len(seq), nil
		case 'D':
			return Event{Key: KeyLeft}, 2 + len(seq), nil
		case 'H':
			return Event{Key: KeyHome}, 2 + len(seq), nil
		case 'F':
			return Event{Key: KeyEnd}, 2 + len(seq), nil
		case '~':
			// Numeric form: ESC [ <n> ~
			num := strings.Trim(string(seq[:len(seq)-1]), ";")
			switch num {
			case "1", "7":
				return Event{Key: KeyHome}, 2 + len(seq), nil
			case "3":
				return Event{Key: KeyDelete}, 2 + len(seq), nil
			case "4", "8":
				return Event{Key: KeyEnd}, 2 + len(seq), nil
			case "5":
				return Event{Key: KeyPageUp}, 2 + len(seq), nil
			case "6":
				return Event{Key: KeyPageDown}, 2 + len(seq), nil
			}
			return Event{Key: KeyUnknown}, 2 + len(seq), nil
		}
		return Event{Key: KeyUnknown}, 2 + len(seq), nil
	}

	if b < 0x20 {
		return Event{Key: KeyUnknown}, 1, nil
	}
	r, size := utf8.DecodeRune(buf)
	if r == utf8.RuneError && size <= 1 {
		return Event{Key: KeyUnknown}, 1, nil
	}
	return Event{Key: KeyRune, Rune: r}, size, nil
}
