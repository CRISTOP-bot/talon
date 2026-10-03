//go:build linux || solaris || aix

package term

import "syscall"

// Linux-style terminal ioctl request numbers.
const (
	ioctlReadTermios  = syscall.TCGETS
	ioctlWriteTermios = syscall.TCSETS
	ioctlGetWinsize   = syscall.TIOCGWINSZ
)
