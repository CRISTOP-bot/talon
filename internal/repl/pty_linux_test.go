//go:build linux

package repl

import (
	"fmt"
	"os"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// Linux ioctl numbers used to set up a pty pair without cgo.
const (
	tiocSPTLCK = 0x40045431
	tiocGPTN   = 0x80045430
	tiocSWINSZ = 0x5414
)

type winsize struct {
	Rows, Cols, X, Y uint16
}

// openPTYPair returns the master and slave ends of a pseudo-terminal sized for
// the full-screen interface.
func openPTYPair(t *testing.T) (*os.File, *os.File) {
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx on this machine: %v", err)
	}
	var unlock int32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), tiocSPTLCK,
		uintptr(unsafe.Pointer(&unlock))); errno != 0 {
		master.Close()
		t.Skipf("cannot unlock the pty: %v", errno)
	}
	var number uint32
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), tiocGPTN,
		uintptr(unsafe.Pointer(&number))); errno != 0 {
		master.Close()
		t.Skipf("cannot read the pty number: %v", errno)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Skipf("cannot open the pty slave: %v", err)
	}
	ws := winsize{Rows: 30, Cols: 100}
	_, _, _ = syscall.Syscall(syscall.SYS_IOCTL, slave.Fd(), tiocSWINSZ,
		uintptr(unsafe.Pointer(&ws)))
	t.Cleanup(func() {
		slave.Close()
		master.Close()
	})
	return master, slave
}

// masterReadDeadline gives Read a deadline on the master side so the test does
// not block forever when the child produced nothing.
func masterReadDeadline(f *os.File, d time.Duration) error {
	return f.SetReadDeadline(time.Now().Add(d))
}
