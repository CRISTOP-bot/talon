package ui

import (
	"io"
	"strings"
	"sync"
)

// MarkdownStream renders a markdown response incrementally. Completed lines
// are styled and printed as soon as the model emits them, so the user sees
// progress instead of a frozen screen. Fenced code blocks are buffered until
// the closing fence so they can be highlighted as a whole.
type MarkdownStream struct {
	Out   io.Writer
	Theme Theme
	// Width limits wrapping; 0 disables it.
	Width int

	mu       sync.Mutex
	pending  []byte
	inFence  bool
	fenceTag string
	fenceBuf []string
	wrote    bool
	done     bool
}

// NewMarkdownStream creates a streaming renderer.
func NewMarkdownStream(out io.Writer, t Theme, width int) *MarkdownStream {
	return &MarkdownStream{Out: out, Theme: t, Width: width}
}

// Write implements io.Writer for streaming tokens.
func (s *MarkdownStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return len(p), nil
	}
	s.pending = append(s.pending, p...)
	for {
		idx := indexByte(s.pending, '\n')
		if idx < 0 {
			break
		}
		line := string(s.pending[:idx])
		s.pending = s.pending[idx+1:]
		s.emitLine(line)
	}
	return len(p), nil
}

// Flush renders any trailing text that arrived without a final newline.
func (s *MarkdownStream) Flush() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return nil
	}
	if len(s.pending) > 0 {
		s.emitLine(string(s.pending))
		s.pending = nil
	}
	if s.inFence && len(s.fenceBuf) > 0 {
		s.closeFence()
	}
	s.done = true
	if s.wrote {
		_, _ = io.WriteString(s.Out, "\n")
	}
	return nil
}

//nolint:gocyclo // dispatch of the small state machine
func (s *MarkdownStream) emitLine(line string) {
	if s.inFence {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			s.closeFence()
		} else {
			s.fenceBuf = append(s.fenceBuf, line)
		}
		return
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "```") {
		s.beginFence(strings.TrimSpace(strings.TrimPrefix(trimmed, "```")))
		return
	}
	if trimmed == "" {
		s.write("\n")
		return
	}
	s.write(s.Theme.Inline(line) + "\n")
}

func (s *MarkdownStream) beginFence(lang string) {
	s.inFence = true
	s.fenceTag = lang
	s.fenceBuf = nil
}

func (s *MarkdownStream) closeFence() {
	pr := &Printer{Out: s.Out, Theme: s.Theme, Width: s.Width}
	body := strings.Join(s.fenceBuf, "\n")
	if body == "" {
		body = "\n"
	}
	s.write("\n")
	s.write(pr.renderCode(s.fenceTag, body) + "\n")
	s.inFence = false
	s.fenceBuf = nil
}

func (s *MarkdownStream) write(text string) {
	_, _ = io.WriteString(s.Out, text)
	s.wrote = true
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// RawStream streams plain text with word wrapping and no markdown processing.
type RawStream struct {
	Out   io.Writer
	Width int

	mu      sync.Mutex
	col     int
	started bool
}

// NewRawStream creates a plain streaming writer.
func NewRawStream(out io.Writer, width int) *RawStream {
	return &RawStream{Out: out, Width: width}
}

// Write implements io.Writer.
func (s *RawStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range p {
		if s.Width > 0 && s.col >= s.Width && b != '\n' {
			if _, err := io.WriteString(s.Out, "\n"); err != nil {
				return 0, err
			}
			s.col = 0
		}
		if _, err := s.Out.Write([]byte{b}); err != nil {
			return 0, err
		}
		if b == '\n' {
			s.col = 0
		} else {
			s.col++
		}
	}
	s.started = true
	return len(p), nil
}

// Started reports whether anything has been written.
func (s *RawStream) Started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}
