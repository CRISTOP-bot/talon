//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package term

// BSD-style terminal ioctl request numbers.
const (
	ioctlReadTermios  = 0x40487413 // TIOCGETA
	ioctlWriteTermios = 0x80487414 // TIOCSETA
	ioctlGetWinsize   = 0x40087468 // TIOCGWINSZ
)
