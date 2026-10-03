//go:build linux

package term

import (
	"fmt"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// openPTY returns a master/slave pseudo-terminal pair.
func openPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx on this machine: %v", err)
	}
	// Unlock the slave side and find its number.
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
	slave, err = os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Skipf("cannot open the pty slave: %v", err)
	}
	t.Cleanup(func() {
		slave.Close()
		master.Close()
	})
	return master, slave
}

// Linux ioctl numbers for the pty slave handshake.
const (
	tiocSPTLCK = 0x40045431
	tiocGPTN   = 0x80045430
)

// TestReadlineReturnsTypedLine drives the editor through a real pty. It is the
// regression test for a zero-length read buffer, which made every read return
// (0, nil) and left the user typing into a program that never read a key.
func TestReadlineReturnsTypedLine(t *testing.T) {
	master, slave := openPTY(t)

	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		rl := NewReadline(slave, slave, nil)
		line, err := rl.ReadLine("> ")
		done <- result{line, err}
	}()

	// The editor needs a moment to enter raw mode before input is delivered.
	time.Sleep(100 * time.Millisecond)
	if _, err := master.WriteString("hola\r"); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("ReadLine returned an error: %v", got.err)
		}
		if got.line != "hola" {
			t.Fatalf("ReadLine returned %q, want %q", got.line, "hola")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadLine never returned: no keystroke was read")
	}
}

// TestReadlineRendersEachKeystroke proves the line is drawn as it is typed, not
// only when it is submitted. The editor writes to the slave, so what it renders
// arrives on the master side of the pty.
func TestReadlineRendersEachKeystroke(t *testing.T) {
	master, slave := openPTY(t)

	if !IsTerminal(slave) {
		t.Skip("the pty slave is not reported as a terminal")
	}

	rendered := make(chan string, 64)
	go func() {
		buf := make([]byte, 512)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				rendered <- string(buf[:n])
			}
			if err != nil {
				close(rendered)
				return
			}
		}
	}()

	done := make(chan string, 1)
	go func() {
		rl := NewReadline(slave, slave, nil)
		line, _ := rl.ReadLine("> ")
		done <- line
	}()

	time.Sleep(150 * time.Millisecond)
	if _, err := master.WriteString("hola"); err != nil {
		t.Fatal(err)
	}

	// Collect what the editor draws. Consecutive redraws may arrive in a single
	// read, so the deadline bounds the wait rather than each keystroke.
	seen := ""
	deadline := time.After(3 * time.Second)
collect:
	for !strings.Contains(seen, "hola") {
		select {
		case chunk, ok := <-rendered:
			if !ok {
				break collect
			}
			seen += chunk
		case <-deadline:
			break collect
		}
	}
	if !strings.Contains(seen, "hola") {
		t.Fatalf("the typed text was never rendered, got %q", seen)
	}
	if _, err := master.WriteString("\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case line := <-done:
		if line != "hola" {
			t.Fatalf("ReadLine returned %q, want %q", line, "hola")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadLine never returned after Enter")
	}
}

// TestPlainFallbackReadsLines proves a non-terminal still works, which is the
// path used when input is piped.
func TestPlainFallbackReadsLines(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() {
		_, _ = w.WriteString("una linea\n")
		_ = w.Close()
	}()
	if IsTerminal(r) {
		t.Skip("a pipe was reported as a terminal")
	}
	rl := NewReadline(r, os.Stdout, nil)
	line, err := rl.ReadLine("> ")
	if err != nil && !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("ReadLine returned an error: %v", err)
	}
	if line != "una linea" {
		t.Fatalf("ReadLine returned %q, want %q", line, "una linea")
	}
}
