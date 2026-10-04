//go:build unix

package term

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// State holds the terminal settings captured before entering raw mode.
type State struct {
	fd  uintptr
	old syscall.Termios
}

// MakeRaw switches the terminal into raw mode and returns the state needed to
// restore it. The caller must always invoke the returned restore function.
func MakeRaw(f *os.File) (*State, error) {
	fd := f.Fd()
	old, err := getTermios(fd)
	if err != nil {
		return nil, fmt.Errorf("read terminal attributes: %w", err)
	}
	raw := *old
	raw.Iflag &^= syscall.IXON | syscall.ICRNL | syscall.BRKINT | syscall.INPCK | syscall.ISTRIP
	// ISIG must go too: with it enabled the terminal turns Ctrl+C into SIGINT
	// and the application never sees the key, so it cannot decide whether to
	// clear the line, interrupt a turn or quit.
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := setTermios(fd, &raw); err != nil {
		return nil, fmt.Errorf("set raw mode: %w", err)
	}
	return &State{fd: fd, old: *old}, nil
}

// Restore returns the terminal to its previous mode.
func (s *State) Restore() error {
	if s == nil {
		return nil
	}
	return setTermios(s.fd, &s.old)
}

func getTermios(fd uintptr) (*syscall.Termios, error) {
	var t syscall.Termios
	if err := ioctlTermios(fd, ioctlReadTermios, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

func setTermios(fd uintptr, t *syscall.Termios) error {
	return ioctlTermios(fd, ioctlWriteTermios, t)
}

func ioctlTermios(fd uintptr, req uintptr, t *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(t)))
	if errno != 0 {
		return errno
	}
	return nil
}

// Size returns the terminal size in columns and rows.
func Size(f *os.File) (cols, rows int, err error) {
	var ws struct{ Row, Col, Xpixel, Ypixel uint16 }
	err = ioctlPtr(f.Fd(), ioctlGetWinsize, unsafe.Pointer(&ws))
	if err != nil {
		return 0, 0, err
	}
	return int(ws.Col), int(ws.Row), nil
}

func ioctlPtr(fd, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}
