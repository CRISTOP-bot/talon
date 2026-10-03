//go:build windows

package term

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	kernel32                       = syscall.NewLazyDLL("kernel32.dll")
	procGetConsoleMode             = kernel32.NewProc("GetConsoleMode")
	procSetConsoleMode             = kernel32.NewProc("SetConsoleMode")
	procGetConsoleScreenBufferInfo = kernel32.NewProc("GetConsoleScreenBufferInfo")
)

const (
	enableProcessedInput  = 0x0001
	enableLineInput       = 0x0002
	enableEchoInput       = 0x0004
	enableVirtualTerminal = 0x0200
)

// State holds the console mode captured before entering raw mode.
type State struct {
	handle syscall.Handle
	old    uint32
}

// MakeRaw disables line editing and echo and turns on ANSI input parsing, so
// the readline editor sees escape sequences for the arrow keys.
func MakeRaw(f *os.File) (*State, error) {
	handle := syscall.Handle(f.Fd())
	var mode uint32
	ret, _, err := procGetConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode)))
	if ret == 0 {
		return nil, fmt.Errorf("read console mode: %w", err)
	}
	raw := mode &^ (enableLineInput | enableEchoInput | enableProcessedInput)
	raw |= enableVirtualTerminal
	ret, _, err = procSetConsoleMode.Call(uintptr(handle), uintptr(raw))
	if ret == 0 {
		return nil, fmt.Errorf("set console mode: %w", err)
	}
	return &State{handle: handle, old: mode}, nil
}

// Restore returns the console to its previous mode.
func (s *State) Restore() error {
	if s == nil {
		return nil
	}
	ret, _, err := procSetConsoleMode.Call(uintptr(s.handle), uintptr(s.old))
	if ret == 0 {
		return err
	}
	return nil
}

type coord struct{ X, Y int16 }

type smallRect struct{ Left, Top, Right, Bottom int16 }

type windowsConsoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

// Size returns the console window size in columns and rows.
func Size(f *os.File) (cols, rows int, err error) {
	handle := syscall.Handle(f.Fd())
	var info windowsConsoleScreenBufferInfo
	ret, _, err := procGetConsoleScreenBufferInfo.Call(
		uintptr(handle), uintptr(unsafe.Pointer(&info)))
	if ret == 0 {
		return 0, 0, fmt.Errorf("read console size: %w", err)
	}
	cols = int(info.Window.Right - info.Window.Left + 1)
	rows = int(info.Window.Bottom - info.Window.Top + 1)
	if cols <= 0 {
		cols = int(info.Size.X)
	}
	if rows <= 0 {
		rows = int(info.Size.Y)
	}
	return cols, rows, nil
}
