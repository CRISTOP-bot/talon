//go:build unix

package repl

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/CRISTOP-bot/talon/internal/term"
)

// watchResize calls onResize whenever the terminal changes size, and returns a
// function that stops watching. Unix delivers SIGWINCH, so this costs nothing
// while the program is idle.
func watchResize(f *os.File, onResize func(cols, rows int)) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ch:
				if cols, rows, err := term.Size(f); err == nil {
					onResize(cols, rows)
				}
			case <-done:
				signal.Stop(ch)
				return
			}
		}
	}()
	return func() { close(done) }
}
