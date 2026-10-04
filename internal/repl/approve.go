package repl

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/agent"
	"github.com/CRISTOP-bot/talon/internal/llm"
	"github.com/CRISTOP-bot/talon/internal/perm"
	"github.com/CRISTOP-bot/talon/internal/ui"
)

// Approve implements agent.Approver: it shows a confirmation panel and waits for
// the user. In auto-approve mode (--yes) it answers yes immediately, and the
// full-screen interface shows the same panel inside the conversation.
func (r *REPL) Approve(ctx context.Context, req perm.Request, call llm.ToolCall) (agent.Approval, error) {
	if r.opts.AutoApprove {
		return agent.Approval{Allow: true, Always: true}, nil
	}
	if a := r.tuiApp; a != nil {
		allow := a.RequestApproval(req.Description, req.Tool, req.Risk.String(), req.Command, req.Path)
		if allow {
			return agent.Approval{Allow: true}, nil
		}
		return agent.Approval{Allow: false, Reason: "the user declined the action"}, nil
	}
	r.spinner.Stop("")
	theme := r.theme

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n", theme.Warn(ui.GlyphWarn), theme.Strong("This action needs your confirmation"))
	if req.Description != "" {
		fmt.Fprintf(&b, "  %s\n", req.Description)
	}
	fmt.Fprintf(&b, "  %-12s %s\n", "tool", req.Tool)
	fmt.Fprintf(&b, "  %-12s %s\n", "risk", req.Risk.String())
	if req.Command != "" {
		fmt.Fprintf(&b, "\n  %s\n", theme.Code+"$ "+req.Command)
	}
	if hits := perm.DangerousCommands(req.Command); len(hits) > 0 {
		fmt.Fprintf(&b, "  %s\n", theme.Err("dangerous patterns: "+strings.Join(hits, ", ")))
	}
	if req.Path != "" {
		fmt.Fprintf(&b, "  %s\n", theme.Code+req.Path)
	}
	r.pr.Line(b.String())
	r.pr.Muted("  [y] once   [a] always allow this tool   [n] reject")

	answer, err := r.readAnswer()
	if err != nil {
		return agent.Approval{Allow: false, Reason: "no answer was given"}, nil
	}
	switch strings.ToLower(answer) {
	case "y", "yes", "":
		return agent.Approval{Allow: true}, nil
	case "a", "always":
		return agent.Approval{Allow: true, Always: true}, nil
	case "n", "no":
		return agent.Approval{Allow: false, Reason: "the user declined the action"}, nil
	default:
		return agent.Approval{Allow: false, Reason: answer}, nil
	}
}

// readAnswer reads one line from stdin without touching the line editor's state.
func (r *REPL) readAnswer() (string, error) {
	in := r.opts.Stdin
	if in == nil {
		in = os.Stdin
	}
	reader := bufio.NewReader(in)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimSpace(line), nil
}
