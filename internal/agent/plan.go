package agent

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/llm"
)

// PlanStep is one numbered step of a plan.
type PlanStep struct {
	Number int
	Text   string
}

// Plan is a parsed plan produced in plan mode.
type Plan struct {
	Steps []PlanStep
	// Raw is the original text.
	Raw string
	// Summary is the first non-empty paragraph before the steps, if any.
	Summary string
}

// String renders the plan as markdown.
func (p *Plan) String() string {
	var b strings.Builder
	if p.Summary != "" {
		b.WriteString(p.Summary)
		b.WriteString("\n\n")
	}
	for _, s := range p.Steps {
		b.WriteString(strconv.Itoa(s.Number) + ". " + s.Text + "\n")
	}
	return strings.TrimSpace(b.String())
}

var stepPattern = regexp.MustCompile(`^\s*(?:(\d+)[.)]|[-*•]\s|\[[ xX]\]\s)\s*`)

// ParsePlan extracts a numbered plan from markdown text. It tolerates prose
// before the list and treats any non-empty line as a step when the model used a
// / different format.
func ParsePlan(text string) *Plan {
	plan := &Plan{Raw: text}
	var intro []string
	lines := strings.Split(text, "\n")
	inList := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "```") {
			continue
		}
		if isPlanLine(trimmed) {
			inList = true
			body := stepPattern.ReplaceAllString(trimmed, "")
			plan.Steps = append(plan.Steps, PlanStep{
				Number: len(plan.Steps) + 1,
				Text:   strings.TrimSpace(body),
			})
			continue
		}
		if inList {
			// A continuation line of the previous step.
			if n := len(plan.Steps); n > 0 {
				plan.Steps[n-1].Text += " " + trimmed
				continue
			}
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		intro = append(intro, trimmed)
	}
	plan.Summary = strings.Join(intro, " ")
	if len(plan.Steps) == 0 && strings.TrimSpace(text) != "" {
		// No recognisable list: treat the whole answer as a single step.
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				plan.Steps = append(plan.Steps, PlanStep{Number: 1, Text: strings.TrimSpace(l)})
			}
		}
	}
	return plan
}

func isPlanLine(trimmed string) bool {
	if stepPattern.MatchString(trimmed) && !strings.HasPrefix(trimmed, "- ") &&
		!strings.HasPrefix(trimmed, "* ") && !strings.HasPrefix(trimmed, "• ") {
		return true // numbered
	}
	if strings.HasPrefix(trimmed, "- ") || strings.HasPrefix(trimmed, "* ") ||
		strings.HasPrefix(trimmed, "• ") || strings.HasPrefix(trimmed, "[ ] ") ||
		strings.HasPrefix(trimmed, "[x] ") || strings.HasPrefix(trimmed, "[X] ") {
		return true
	}
	return false
}

// PlanMode runs a turn in plan mode: read-only tools are allowed, changes are
// not, and the final answer is parsed into a Plan.
func (a *Agent) PlanMode(ctx context.Context, input string) (*Plan, error) {
	previous := a.mode
	a.mode = ModePlan
	defer func() { a.mode = previous }()

	msg, err := a.Run(ctx, input)
	if err != nil {
		return nil, err
	}
	plan := ParsePlan(msg.Content)
	a.emit(Event{Kind: EventPlan, Plan: plan})
	return plan, nil
}

// Execute runs a plan step by step, feeding each confirmed step to the agent as
// a separate turn. The caller decides whether to continue after each step.
type ExecuteOptions struct {
	// MaxStepsPerItem limits the agent loop per plan step.
	MaxStepsPerItem int
	// BeforeStep is called before each step; returning an error aborts.
	BeforeStep func(step PlanStep, index int) error
	// AfterStep is called after each completed step.
	AfterStep func(step PlanStep, index int, reply string)
}

// ExecutePlan runs the steps of a plan sequentially.
func (a *Agent) ExecutePlan(ctx context.Context, plan *Plan, opts ExecuteOptions) error {
	for i, step := range plan.Steps {
		if opts.BeforeStep != nil {
			if err := opts.BeforeStep(step, i+1); err != nil {
				return err
			}
		}
		prompt := fmt.Sprintf("Execute step %d of %d of the plan: %s\n\n"+
			"Finish only this step, then report what you did in one or two sentences.",
			step.Number, len(plan.Steps), step.Text)
		msg, err := a.Run(ctx, prompt)
		if err != nil {
			return err
		}
		if opts.AfterStep != nil {
			opts.AfterStep(step, i+1, msg.Content)
		}
	}
	return nil
}

// Compact asks the model to summarise the older part of the conversation and
// replaces it with the digest. It returns the number of messages dropped.
func (a *Agent) Compact(ctx context.Context) (int, error) {
	total := a.memory.Len()
	if total < 4 {
		return 0, nil
	}
	cut := total / 2
	older := a.memory.SliceUntil(cut)
	if len(older) < 2 {
		return 0, nil
	}
	digest, err := a.summarize(ctx, older)
	if err != nil {
		// Fall back to a mechanical digest rather than losing the history.
		digest = Summarize(older)
	}
	a.memory.SetSummary(strings.TrimSpace(digest), len(older))
	return len(older), nil
}

func (a *Agent) summarize(ctx context.Context, msgs []llm.Message) (string, error) {
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case llm.RoleUser:
			b.WriteString("user: " + oneLine(m.Content, 300) + "\n")
		case llm.RoleAssistant:
			for _, call := range m.ToolCalls {
				b.WriteString("assistant called " + call.Name + "(" + oneLine(call.Arguments, 200) + ")\n")
			}
			if strings.TrimSpace(m.Content) != "" {
				b.WriteString("assistant: " + oneLine(m.Content, 300) + "\n")
			}
		case llm.RoleTool:
			b.WriteString("tool " + m.Name + " returned: " + oneLine(m.Content, 200) + "\n")
		}
	}
	req := llm.Request{
		Model: "summary",
		System: "Summarize the conversation below for an autonomous coding agent. " +
			"Keep: the user's goal, decisions taken, files created or modified, commands run and their results, " +
			"and anything still unfinished. Be specific about file paths. Write plain prose or bullets, no preamble.",
		Messages:    []llm.Message{{Role: llm.RoleUser, Content: b.String()}},
		Temperature: 0.1,
		MaxTokens:   800,
	}
	resp, err := llm.CollectStream(ctx, a.opts.Provider, req, nil)
	if err != nil {
		return "", err
	}
	return resp.Message.Content, nil
}
