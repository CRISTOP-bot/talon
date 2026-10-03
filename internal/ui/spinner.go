package ui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// SpinnerFrames is the animation used while the agent is working.
var SpinnerFrames = []string{"◐", "◓", "◑", "◒"}

// Spinner renders an animated status line. It is safe to update the message
// from another goroutine, which is how the agent reports tool activity while a
// request is in flight.
type Spinner struct {
	Out   io.Writer
	Theme Theme
	// Interval between frames; defaults to 100ms.
	Interval time.Duration
	// Enabled turns the animation off (still prints the message once).
	Enabled bool

	mu      sync.Mutex
	message string
	frame   int
	running bool
	lastLen int
	done    chan struct{}
	wg      sync.WaitGroup
	printed bool
}

// NewSpinner creates a spinner writing to out.
func NewSpinner(out io.Writer, t Theme, enabled bool) *Spinner {
	return &Spinner{Out: out, Theme: t, Interval: 100 * time.Millisecond, Enabled: enabled}
}

// Start begins the animation.
func (s *Spinner) Start(msg string) {
	s.mu.Lock()
	if s.running {
		s.message = msg
		s.mu.Unlock()
		return
	}
	s.running = true
	s.message = msg
	s.done = make(chan struct{})
	s.mu.Unlock()
	if !s.Enabled {
		return
	}
	s.wg.Add(1)
	go s.run()
}

func (s *Spinner) run() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()
	for {
		s.mu.Lock()
		select {
		case <-s.done:
			s.mu.Unlock()
			return
		default:
		}
		frame := SpinnerFrames[s.frame%len(SpinnerFrames)]
		s.frame++
		line := s.Theme.Paint(s.Theme.Primary, frame) + " " + s.message
		pad := s.lastLen - DisplayWidth(line)
		if pad < 0 {
			pad = 0
		}
		fmt.Fprintf(s.Out, "\r\x1b[K%s%s", line, spaces(pad))
		s.lastLen = DisplayWidth(line)
		s.printed = true
		s.mu.Unlock()
		<-ticker.C
	}
}

// Update replaces the spinner message.
func (s *Spinner) Update(msg string) {
	s.mu.Lock()
	s.message = msg
	printIt := !s.Enabled && s.running
	s.mu.Unlock()
	if printIt {
		// Without animation the message is still surfaced once.
		fmt.Fprintf(s.Out, "%s\n", msg)
	}
}

// Stop halts the animation and erases the spinner line. The final line is always
// printed, whether or not the animation was running: with animation disabled, or
// when the work finished before the first frame, it is the only trace of what
// happened.
func (s *Spinner) Stop(final string) {
	s.mu.Lock()
	wasRunning := s.running
	if wasRunning && s.done != nil {
		close(s.done)
		s.running = false
	}
	s.mu.Unlock()
	if wasRunning {
		s.wg.Wait()
	}
	s.mu.Lock()
	s.erase()
	s.mu.Unlock()
	if final != "" {
		fmt.Fprintf(s.Out, "%s\n", final)
	}
}

// Erase removes the spinner line if it is currently displayed.
func (s *Spinner) Erase() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.erase()
}

func (s *Spinner) erase() {
	if s.printed {
		fmt.Fprint(s.Out, "\r\x1b[K")
		s.printed = false
		s.lastLen = 0
	}
}

func spaces(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = ' '
	}
	return string(b)
}

// StepLogger prints one line per completed step, replacing a spinner when one is
// running. It keeps the transcript readable during long agent runs.
type StepLogger struct {
	Printer *Printer
	Spinner *Spinner
}

// Log records a step.
func (l *StepLogger) Log(glyph, style, label, msg string) {
	if l == nil || l.Printer == nil {
		return
	}
	if l.Spinner != nil {
		l.Spinner.Stop("")
	}
	l.Printer.Status(glyph, style, label, msg)
}
