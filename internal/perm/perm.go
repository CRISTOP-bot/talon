// Package perm implements Talon's permission model: it decides whether a tool
// call may run, needs confirmation, or must be refused.
//
// Four levels are supported:
//
//	read-only      tools that only read are allowed; everything else is denied
//	safe           read + write inside the workspace; commands need approval
//	confirm        (default) automatic for safe operations, prompt otherwise
//	full-access    everything allowed, except an explicit deny list
package perm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Level is a permission level.
type Level string

// Permission levels.
const (
	ReadOnly   Level = "read-only"
	Safe       Level = "safe"
	Confirm    Level = "confirm"
	FullAccess Level = "full-access"
)

// Risk classifies an operation.
type Risk int

// Risk levels.
const (
	// RiskRead only reads data.
	RiskRead Risk = iota
	// RiskWrite modifies files inside the workspace.
	RiskWrite
	// RiskExec runs commands.
	RiskExec
	// RiskDanger performs destructive or irreversible operations.
	RiskDanger
)

func (r Risk) String() string {
	switch r {
	case RiskRead:
		return "read"
	case RiskWrite:
		return "write"
	case RiskExec:
		return "exec"
	default:
		return "danger"
	}
}

// Decision is the outcome of a permission check.
type Decision int

// Decisions.
const (
	// Allow means the operation may run without asking.
	Allow Decision = iota
	// Ask means the user must confirm.
	Ask
	// Deny means the operation is refused.
	Deny
)

func (d Decision) String() string {
	switch d {
	case Allow:
		return "allow"
	case Ask:
		return "ask"
	default:
		return "deny"
	}
}

// Request describes the operation being checked.
type Request struct {
	// Tool is the tool name, e.g. "run_command".
	Tool string
	Risk Risk
	// Path is the workspace-relative path for filesystem operations.
	Path string
	// Command is the shell command for command execution.
	Command string
	// Description is a human-readable summary shown in the prompt.
	Description string
}

// Policy evaluates requests against the configured level and rules.
type Policy struct {
	level Level
	// workspace is the absolute project root; operations may not escape it
	// unless an allow rule says otherwise.
	workspace string
	// allowTools and denyTools short-circuit the decision.
	allowTools map[string]bool
	denyTools  map[string]bool
	// allowPaths and denyPaths are glob patterns on absolute paths.
	allowPaths []string
	denyPaths  []string
	// allowCommands and denyCommands are case-insensitive substrings.
	allowCommands []string
	denyCommands  []string
	// alwaysAsk forces confirmation even in full-access mode.
	alwaysAsk map[string]bool
	// sessionAllow remembers one-off approvals granted for the session.
	sessionAllow map[string]bool
	// remembered remembers "always allow" answers.
	remembered map[string]bool
}

// Config configures a Policy.
type Config struct {
	Level          Level
	Workspace      string
	AllowTools     []string
	DenyTools      []string
	AllowPaths     []string
	DenyPaths      []string
	AllowCommands  []string
	DenyCommands   []string
	AlwaysAskTools []string
}

// New builds a Policy.
func New(cfg Config) *Policy {
	p := &Policy{
		level:         cfg.Level,
		workspace:     cfg.Workspace,
		allowTools:    map[string]bool{},
		denyTools:     map[string]bool{},
		alwaysAsk:     map[string]bool{},
		sessionAllow:  map[string]bool{},
		remembered:    map[string]bool{},
		allowPaths:    expandPatterns(cfg.AllowPaths, cfg.Workspace),
		denyPaths:     expandPatterns(cfg.DenyPaths, cfg.Workspace),
		allowCommands: lowerAll(cfg.AllowCommands),
		denyCommands:  lowerAll(cfg.DenyCommands),
	}
	for _, t := range cfg.AllowTools {
		p.allowTools[t] = true
	}
	for _, t := range cfg.DenyTools {
		p.denyTools[t] = true
	}
	for _, t := range cfg.AlwaysAskTools {
		p.alwaysAsk[t] = true
	}
	return p
}

// Level returns the active level.
func (p *Policy) Level() Level { return p.level }

// SetLevel changes the level at runtime (the /permissions command).
func (p *Policy) SetLevel(l Level) { p.level = l }

// Workspace returns the project root.
func (p *Policy) Workspace() string { return p.workspace }

// Decision evaluates a request, returning the decision plus the reason, which
// the UI shows to the user.
func (p *Policy) Decision(req Request) (Decision, string) {
	if p.denyTools[req.Tool] {
		return Deny, fmt.Sprintf("%s is on the deny list", req.Tool)
	}
	if p.remembered[req.Tool] || p.sessionAllow[req.Tool] {
		if p.level != ReadOnly {
			return Allow, "approved earlier in this session"
		}
	}

	// Deny lists are always enforced, at every level.
	if req.Path != "" {
		if matchAny(p.denyPaths, req.Path) {
			return Deny, fmt.Sprintf("%s matches a deny path rule", req.Path)
		}
		if !p.insideWorkspace(req.Path) && !matchAny(p.allowPaths, req.Path) {
			return Deny, fmt.Sprintf("%s is outside the project directory", req.Path)
		}
	}
	if req.Command != "" {
		lower := strings.ToLower(req.Command)
		if containsAny(lower, p.denyCommands) {
			return Deny, fmt.Sprintf("%q matches a deny rule", req.Command)
		}
	}

	switch p.level {
	case ReadOnly:
		switch req.Risk {
		case RiskRead:
			return Allow, "read-only mode"
		default:
			return Deny, "read-only mode blocks " + req.Risk.String() + " operations"
		}
	case Safe:
		switch req.Risk {
		case RiskRead, RiskWrite:
			return Allow, "safe mode"
		case RiskExec:
			return Ask, "commands require confirmation in safe mode"
		default:
			return Ask, "destructive operations require confirmation"
		}
	case Confirm:
		switch req.Risk {
		case RiskRead:
			return Allow, "reading is always allowed"
		case RiskWrite:
			return Ask, "modifying files requires confirmation"
		case RiskExec:
			return Ask, "running commands requires confirmation"
		default:
			return Ask, "destructive operations require confirmation"
		}
	case FullAccess:
		if p.alwaysAsk[req.Tool] || req.Risk == RiskDanger {
			return Ask, "always confirm this tool"
		}
		return Allow, "full-access mode"
	}
	return Ask, "unknown permission level " + string(p.level)
}

// insideWorkspace reports whether an absolute path is inside the project root.
func (p *Policy) insideWorkspace(path string) bool {
	if p.workspace == "" {
		return true
	}
	abs := path
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(p.workspace, abs)
	}
	rel, err := filepath.Rel(p.workspace, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// AllowOnce approves a tool for the rest of the session.
func (p *Policy) AllowOnce(tool string) { p.sessionAllow[tool] = true }

// AllowAlways approves a tool permanently for this project (in-memory; it is
// not written to disk unless the user runs /permissions save).
func (p *Policy) AllowAlways(tool string) { p.remembered[tool] = true }

// Forget removes remembered approvals.
func (p *Policy) Forget(tool string) {
	delete(p.remembered, tool)
	delete(p.sessionAllow, tool)
}

// Summarize renders the effective policy for the /permissions command.
func (p *Policy) Summarize() string {
	var b strings.Builder
	fmt.Fprintf(&b, "level: %s\n", p.level)
	if len(p.denyTools) > 0 {
		fmt.Fprintf(&b, "denied tools: %s\n", keys(p.denyTools))
	}
	if len(p.allowTools) > 0 {
		fmt.Fprintf(&b, "allowed tools: %s\n", keys(p.allowTools))
	}
	if len(p.denyPaths) > 0 {
		fmt.Fprintf(&b, "denied paths: %s\n", strings.Join(p.denyPaths, ", "))
	}
	if len(p.denyCommands) > 0 {
		fmt.Fprintf(&b, "denied commands: %s\n", strings.Join(p.denyCommands, ", "))
	}
	if len(p.remembered) > 0 {
		fmt.Fprintf(&b, "remembered approvals: %s\n", keys(p.remembered))
	}
	return b.String()
}

// DangerousCommands lists patterns that are always worth a second look, even in
// full-access mode. It is used to warn the user.
func DangerousCommands(command string) []string {
	lower := strings.ToLower(command)
	var hits []string
	for _, pattern := range []string{
		"rm -rf", "sudo", "chmod 777", "chown", "dd if=", "mkfs", ":(){", "shutdown",
		"reboot", "git push", "git reset --hard", "git clean -fd", "kill -9",
		"curl ", "wget ", "nc ", "> /dev/sd", "history -c", "aws ", "gcloud ",
	} {
		if strings.Contains(lower, pattern) {
			hits = append(hits, pattern)
		}
	}
	return hits
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func lowerAll(items []string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, strings.ToLower(s))
	}
	return out
}

func containsAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if n != "" && strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

func matchAny(patterns []string, path string) bool {
	for _, p := range patterns {
		if ok, err := filepath.Match(p, path); err == nil && ok {
			return true
		}
		// A prefix rule covers everything below it.
		if path == p || strings.HasPrefix(path, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}

func expandPatterns(patterns []string, workspace string) []string {
	out := make([]string, 0, len(patterns))
	for _, p := range patterns {
		if strings.HasPrefix(p, "~/") {
			if home := homeDir(); home != "" {
				p = filepath.Join(home, p[2:])
			}
		}
		if !filepath.IsAbs(p) && workspace != "" {
			p = filepath.Join(workspace, p)
		}
		out = append(out, filepath.Clean(p))
	}
	return out
}

func homeDir() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return h
}
