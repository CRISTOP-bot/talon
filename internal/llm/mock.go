package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// Mock is a deterministic, offline provider used by the test suite and by
// `talon --demo` so the CLI can be exercised without network access. It
// scripts tool calls based on the conversation and the available tools, so the
// whole agent loop runs for real against it.
type Mock struct {
	opts Options
	// Script forces a specific sequence of turns. Each entry is the raw JSON
	// the mock will "return": {"text": "...", "tools": [{"name":..,"arguments":{..}}]}
	Script []MockTurn
	// Recorded collects the requests it received, for assertions.
	Recorded []Request
	// MaxTurns stops the agent loop after N turns when Script is empty.
	MaxTurns int

	mu sync.Mutex
}

// MockTurn is one scripted assistant reply.
type MockTurn struct {
	Text string
	// Tools are tool calls to emit after the text.
	Tools []MockToolCall
	// Error, when set, is delivered as an error event instead of a reply.
	Error string
}

// MockToolCall is a scripted tool invocation.
type MockToolCall struct {
	Name      string
	Arguments map[string]any
}

// NewMock builds the mock provider.
func NewMock(opts Options) *Mock {
	return &Mock{opts: opts, MaxTurns: 3}
}

// Name implements Provider.
func (p *Mock) Name() string { return "mock" }

// ListModels implements Provider.
func (p *Mock) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return []ModelInfo{{ID: "mock-coder", DisplayName: "Deterministic offline test model", ContextWindow: 200000}}, nil
}

// Stream implements Provider.
func (p *Mock) Stream(ctx context.Context, req Request, onEvent func(StreamEvent) error) error {
	p.mu.Lock()
	p.Recorded = append(p.Recorded, req)
	turn := len(p.Recorded) - 1
	p.mu.Unlock()

	if turn < len(p.Script) {
		return p.emit(ctx, p.Script[turn], onEvent)
	}
	if p.MaxTurns > 0 && turn >= p.MaxTurns {
		return p.emit(ctx, MockTurn{Text: "Nothing left to do."}, onEvent)
	}
	// Fall back to a generic, deterministic behaviour: read a file if the user
	// mentioned one, otherwise summarize and finish.
	return p.emit(ctx, p.defaultTurn(req), onEvent)
}

// defaultTurn derives a sensible reply from the conversation.
func (p *Mock) defaultTurn(req Request) MockTurn {
	if len(req.Messages) == 0 {
		return MockTurn{Text: "Ready."}
	}
	last := req.Messages[len(req.Messages)-1]
	switch last.Role {
	case RoleTool:
		return MockTurn{Text: fmt.Sprintf("Observed the result of %s.", last.Name)}
	case RoleUser:
		for _, t := range req.Tools {
			if t.Name == "read_file" {
				for _, word := range strings.Fields(last.Content) {
					if strings.Contains(word, ".") && !strings.HasSuffix(word, "?") {
						return MockTurn{
							Text: "Reading the file.",
							Tools: []MockToolCall{{
								Name:      "read_file",
								Arguments: map[string]any{"path": strings.Trim(word, "`\"',")},
							}},
						}
					}
				}
			}
		}
		return MockTurn{Text: "Understood. " + summarise(last.Content)}
	default:
		return MockTurn{Text: "Nothing to do."}
	}
}

func summarise(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 120 {
		s = s[:120] + "…"
	}
	return s
}

func (p *Mock) emit(ctx context.Context, turn MockTurn, onEvent func(StreamEvent) error) error {
	if turn.Error != "" {
		return onEvent(StreamEvent{Type: EventError, Err: ErrNoResponse})
	}
	if turn.Text != "" {
		// Emit in small chunks so streaming paths are exercised.
		words := strings.SplitAfter(turn.Text, " ")
		for _, w := range words {
			if err := onEvent(StreamEvent{Type: EventText, Text: w}); err != nil {
				return err
			}
		}
	}
	for i, tc := range turn.Tools {
		args := "{}"
		if tc.Arguments != nil {
			encoded, err := json.Marshal(tc.Arguments)
			if err != nil {
				return err
			}
			args = string(encoded)
		}
		call := ToolCall{
			ID:        fmt.Sprintf("mock_call_%d", i+1),
			Name:      tc.Name,
			Arguments: args,
		}
		if err := onEvent(StreamEvent{Type: EventToolCall, ToolCall: &call}); err != nil {
			return err
		}
	}
	usage := Usage{InputTokens: 100, OutputTokens: len(turn.Text) / 4}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	if err := onEvent(StreamEvent{Type: EventUsage, Usage: &usage}); err != nil {
		return err
	}
	stop := StopEnd
	if len(turn.Tools) > 0 {
		stop = StopToolUse
	}
	final := Response{
		Message:    Message{Role: RoleAssistant, Content: turn.Text},
		Usage:      usage,
		StopReason: stop,
		Model:      "mock-coder",
	}
	return onEvent(StreamEvent{Type: EventDone, Response: &final, StopReason: stop})
}

// Complete implements Provider.
func (p *Mock) Complete(ctx context.Context, req Request) (Response, error) {
	return Complete(ctx, p, req)
}
