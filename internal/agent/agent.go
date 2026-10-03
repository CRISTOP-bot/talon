// Package agent implements Talon's agent loop: it assembles context, calls the
// model, runs the tools it asks for, feeds the results back and repeats until
// the task is done or a limit is reached.
//
// The loop is deliberately explicit rather than clever: every step is a
// recorded event the UI and the session log can render.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/talon-cli/talon/internal/errs"
	"github.com/talon-cli/talon/internal/llm"
	"github.com/talon-cli/talon/internal/logger"
	"github.com/talon-cli/talon/internal/perm"
	"github.com/talon-cli/talon/internal/tools"
)

// Mode selects how the agent behaves for one turn.
type Mode string

// Agent modes.
const (
	// ModeAsk executes read-only tools freely and asks for everything else.
	ModeAsk Mode = "ask"
	// ModeAuto executes safe operations automatically and asks for dangerous ones.
	ModeAuto Mode = "auto"
	// ModePlan only produces a plan; no tool that changes anything runs.
	ModePlan Mode = "plan"
)

// Approver decides whether a tool call may run. Returning false cancels the
// call, which is reported back to the model as a tool result.
type Approver interface {
	// Approve is called when the permission policy requires confirmation.
	Approve(ctx context.Context, req perm.Request, call llm.ToolCall) (Approval, error)
}

// Approval is the user's answer to a confirmation prompt.
type Approval struct {
	// Allow runs the call once.
	Allow bool
	// Always applies to the rest of the session.
	Always bool
	// Reason is shown to the model when the call is rejected.
	Reason string
}

// Options configures an Agent.
type Options struct {
	Provider llm.Provider
	Tools    *tools.Registry
	ToolCtx  *tools.Context
	System   string
	MaxSteps int
	Mode     Mode
	Approver Approver
	Log      *logger.Logger
	// Temperature and MaxTokens override the configuration when non-zero.
	Temperature float64
	MaxTokens   int
	// OnEvent receives progress events for the UI.
	OnEvent func(Event)
	// PlanOnly forces plan mode regardless of Mode.
	PlanOnly bool
}

// Agent runs the reasoning loop.
type Agent struct {
	opts   Options
	mode   Mode
	memory *Conversation

	mu        sync.Mutex
	steps     int
	lastUsage llm.Usage
}

// EventKind classifies a progress event.
type EventKind string

// Event kinds emitted to the UI.
const (
	EventTurnStart    EventKind = "turn_start"
	EventStatus       EventKind = "status"
	EventText         EventKind = "text"
	EventReasoning    EventKind = "reasoning"
	EventToolStart    EventKind = "tool_start"
	EventToolApproval EventKind = "tool_approval"
	EventToolResult   EventKind = "tool_result"
	EventPlan         EventKind = "plan"
	EventTurnEnd      EventKind = "turn_end"
	EventError        EventKind = "error"
	EventNotice       EventKind = "notice"
)

// Event is one progress notification.
type Event struct {
	Kind EventKind
	// Text is the streamed assistant text (EventText) or a status message.
	Text string
	// Tool names the tool for tool events.
	Tool string
	// Call is the model request, when relevant.
	Call *llm.ToolCall
	// Result is the tool outcome.
	Result *tools.Result
	// Err is set for EventError.
	Err error
	// Plan holds the parsed plan in plan mode.
	Plan *Plan
	// Step is the 1-based step counter.
	Step int
	// Usage is the token usage of the finished turn.
	Usage llm.Usage
}

// New creates an agent.
func New(opts Options) *Agent {
	if opts.MaxSteps <= 0 {
		opts.MaxSteps = 24
	}
	if opts.Mode == "" {
		opts.Mode = ModeAsk
	}
	if opts.Log == nil {
		opts.Log = logger.Discard
	}
	return &Agent{
		opts:   opts,
		mode:   opts.Mode,
		memory: NewConversation(opts.System, opts.MaxTokens),
	}
}

// Mode returns the current mode.
func (a *Agent) Mode() Mode { return a.mode }

// SetMode changes the mode for subsequent turns.
func (a *Agent) SetMode(m Mode) { a.mode = m }

// Conversation exposes the message history (used by sessions and /context).
func (a *Agent) Conversation() *Conversation { return a.memory }

// Steps returns how many model round-trips the last turn used.
func (a *Agent) Steps() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.steps
}

// LastUsage returns the token usage of the last turn.
func (a *Agent) LastUsage() llm.Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastUsage
}

// SetSystem replaces the system prompt used for the next request.
func (a *Agent) SetSystem(p string) { a.memory.System = p }

// System returns the current system prompt.
func (a *Agent) System() string { return a.memory.System }

// Reset clears the conversation, keeping the system prompt.
func (a *Agent) Reset() { a.memory.Reset() }

// AddMessage appends a message to the conversation.
func (a *Agent) AddMessage(m llm.Message) { a.memory.Append(m) }

// Run executes one user turn. It streams assistant text through OnEvent and
// returns the final assistant message.
func (a *Agent) Run(ctx context.Context, userInput string) (llm.Message, error) {
	a.mu.Lock()
	a.steps = 0
	a.lastUsage = llm.Usage{}
	a.mu.Unlock()

	a.memory.Append(llm.Message{Role: llm.RoleUser, Content: userInput})
	a.emit(Event{Kind: EventTurnStart, Text: userInput})

	var final llm.Message
	for step := 1; step <= a.opts.MaxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return llm.Message{}, errs.Cancelled("agent", "the turn was cancelled")
		}
		a.mu.Lock()
		a.steps = step
		a.mu.Unlock()

		assistant, toolCalls, usage, err := a.callModel(ctx, step)
		if err != nil {
			a.emit(Event{Kind: EventError, Err: err})
			return llm.Message{}, err
		}
		a.mu.Lock()
		a.lastUsage = usage
		a.mu.Unlock()

		// Record the assistant message (text plus requested tool calls).
		a.memory.Append(assistant)

		if len(toolCalls) == 0 {
			a.emit(Event{Kind: EventTurnEnd, Step: step, Usage: usage})
			final = assistant
			return assistant, nil
		}

		if assistant.Content != "" {
			final = assistant
		}
		for i := range toolCalls {
			if err := a.runTool(ctx, step, toolCalls[i], i); err != nil {
				if errs.IsKind(err, errs.KindCancelled) {
					return llm.Message{}, err
				}
				// A tool error is reported to the model so it can recover.
				a.memory.Append(toolErrorMessage(toolCalls[i], err))
				a.emit(Event{Kind: EventNotice, Text: fmt.Sprintf("%s failed: %v", toolCalls[i].Name, err)})
				continue
			}
		}
		if step == a.opts.MaxSteps {
			msg := fmt.Sprintf("Stopped after %d steps without finishing. Review the state and continue.", a.opts.MaxSteps)
			a.memory.Append(llm.Message{Role: llm.RoleUser, Content: msg})
			a.emit(Event{Kind: EventNotice, Text: msg})
		}
	}

	final = a.memory.LastAssistant()
	a.emit(Event{Kind: EventTurnEnd, Step: a.Steps(), Usage: a.LastUsage()})
	return final, nil
}

// runTool performs one tool call: validate, ask for permission, execute and
// record the result.
func (a *Agent) runTool(ctx context.Context, step int, call llm.ToolCall, index int) error {
	def, ok := a.opts.Tools.Get(call.Name)
	if !ok {
		err := fmt.Errorf("unknown tool %q; available tools: %s", call.Name, strings.Join(a.opts.Tools.Names(), ", "))
		a.memory.Append(toolErrorMessage(call, err))
		return err
	}
	a.emit(Event{Kind: EventToolStart, Step: step, Tool: call.Name, Call: &call})

	req := def.Request(json.RawMessage(call.Arguments))
	if a.mode == ModePlan && req.Risk > perm.RiskRead {
		a.emit(Event{
			Kind: EventToolApproval,
			Tool: call.Name,
			Text: "skipped in plan mode",
			Call: &call,
		})
		a.memory.Append(llm.Message{
			Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID,
			Content: "Plan mode is active: changes are not applied. Describe in your final answer " +
				"exactly which edits would be needed.",
		})
		return nil
	}

	decision, reason := a.opts.ToolCtx.Policy.Decision(req)
	if decision == perm.Deny {
		a.emit(Event{Kind: EventToolApproval, Tool: call.Name, Text: "denied: " + reason, Call: &call})
		a.memory.Append(llm.Message{
			Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID,
			Content: "Permission denied: " + reason + ". Choose a different approach or ask the user.",
		})
		return fmt.Errorf("permission denied: %s", reason)
	}
	if decision == perm.Ask && a.opts.Approver != nil {
		approval, err := a.opts.Approver.Approve(ctx, req, call)
		if err != nil {
			return err
		}
		if approval.Always {
			a.opts.ToolCtx.Policy.AllowAlways(call.Name)
		} else if approval.Allow {
			a.opts.ToolCtx.Policy.AllowOnce(call.Name)
		}
		if !approval.Allow {
			text := approval.Reason
			if text == "" {
				text = "the user declined this action"
			}
			a.emit(Event{Kind: EventToolApproval, Tool: call.Name, Text: "rejected: " + text, Call: &call})
			a.memory.Append(llm.Message{
				Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID,
				Content: "The user rejected this action: " + text,
			})
			return fmt.Errorf("rejected by the user: %s", text)
		}
		a.emit(Event{Kind: EventToolApproval, Tool: call.Name, Text: "approved", Call: &call})
	}

	res, err := def.Run(a.opts.ToolCtx, json.RawMessage(call.Arguments))
	if err != nil {
		a.emit(Event{Kind: EventToolResult, Step: step, Tool: call.Name, Err: err, Call: &call})
		return err
	}
	a.emit(Event{Kind: EventToolResult, Step: step, Tool: call.Name, Result: &res, Call: &call})
	a.memory.Append(llm.Message{
		Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID, Content: res.Content,
	})
	return nil
}

// callModel performs one streaming request and returns the assistant message,
// its tool calls and the usage.
func (a *Agent) callModel(ctx context.Context, step int) (llm.Message, []llm.ToolCall, llm.Usage, error) {
	req := a.buildRequest()
	a.opts.Log.Debugf("agent step %d: %d messages, %d tools", step, len(req.Messages), len(req.Tools))

	var content, reasoning strings.Builder
	var calls []llm.ToolCall
	var usage llm.Usage
	var streamErr error

	started := time.Now()
	err := a.opts.Provider.Stream(ctx, req, func(ev llm.StreamEvent) error {
		switch ev.Type {
		case llm.EventText:
			content.WriteString(ev.Text)
			a.emit(Event{Kind: EventText, Text: ev.Text, Step: step})
		case llm.EventReasoning:
			reasoning.WriteString(ev.Text)
			a.emit(Event{Kind: EventReasoning, Text: ev.Text, Step: step})
		case llm.EventToolCall:
			if ev.ToolCall != nil {
				calls = append(calls, *ev.ToolCall)
			}
		case llm.EventUsage:
			if ev.Usage != nil {
				usage = *ev.Usage
			}
		case llm.EventError:
			streamErr = ev.Err
			return errStopStream
		}
		return nil
	})
	if streamErr != nil {
		return llm.Message{}, nil, usage, streamErr
	}
	if err != nil && err != errStopStream {
		return llm.Message{}, nil, usage, err
	}
	a.opts.Log.Debugf("agent step %d finished in %s (%d tool calls)", step, time.Since(started), len(calls))

	msg := llm.Message{
		Role:      llm.RoleAssistant,
		Content:   content.String(),
		Reasoning: reasoning.String(),
		ToolCalls: calls,
	}
	return msg, calls, usage, nil
}

// buildRequest assembles the model request for the current turn.
func (a *Agent) buildRequest() llm.Request {
	system := a.memory.System
	if a.mode == ModePlan {
		system = planSystemSuffix(system)
	}
	req := llm.Request{
		System:      system,
		Messages:    a.memory.Messages(),
		Tools:       a.opts.Tools.Specs(),
		Temperature: a.opts.Temperature,
		MaxTokens:   a.opts.MaxTokens,
	}
	if a.mode == ModePlan {
		// Plan mode must not be able to change anything: only read-only tools.
		req.Tools = a.readOnlySpecs()
	}
	return req
}

func (a *Agent) emit(ev Event) {
	if a.opts.OnEvent == nil {
		return
	}
	a.opts.OnEvent(ev)
}

// toolErrorMessage renders a failed tool call for the model.
func toolErrorMessage(call llm.ToolCall, err error) llm.Message {
	return llm.Message{
		Role: llm.RoleTool, Name: call.Name, ToolCallID: call.ID,
		Content: "error: " + err.Error() + "\nFix the arguments or choose another approach.",
	}
}

// errStopStream stops a stream without treating it as a failure.
var errStopStream = fmt.Errorf("stop stream")

// readOnlySpecs returns the specs of tools that cannot change anything. It uses
// the tool definitions, so a mislabelled tool name cannot leak into plan mode.
func (a *Agent) readOnlySpecs() []llm.ToolSpec {
	var out []llm.ToolSpec
	for _, d := range a.opts.Tools.All() {
		if d.ReadOnly || d.Risk == perm.RiskRead {
			out = append(out, d.Spec())
		}
	}
	return out
}

func planSystemSuffix(system string) string {
	return system + `

PLAN MODE IS ACTIVE.
- You may use read-only tools to understand the project.
- You must not create, edit or delete anything, and must not run commands.
- Finish with a numbered plan: each step says what to change in which file and how to verify it.
- Keep the plan concrete and short enough to be executed in one session.`
}
