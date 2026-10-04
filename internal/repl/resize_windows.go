//go:build windows

package repl

import (
	"os"
	"time"

	"github.com/CRISTOP-bot/talon/internal/term"
)

// watchResize polls the terminal size, because Windows has no SIGWINCH. The poll
// interval is short enough to feel immediate and long enough to cost nothing.
func watchResize(f *os.File, onResize func(cols, rows int)) func() {
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		lastCols, lastRows := 0, 0
		for {
			select {
			case <-ticker.C:
				cols, rows, err := term.Size(f)
				if err != nil {
					continue
				}
				if cols != lastCols || rows != lastRows {
					lastCols, lastRows = cols, rows
					onResize(cols, rows)
				}
			case <-done:
				return
			}
		}
	}()
	return func() { close(done) }
}
