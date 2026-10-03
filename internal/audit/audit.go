// Package audit records security-relevant events in a local, append-only log.
//
// What goes in: permission decisions, tool executions, network attempts,
// secret redactions, injection findings, sandbox application, consent changes
// and data deletion. What stays out: conversation content, file contents and
// anything that is not a decision or an action.
//
// The log is local only. It is never uploaded, and Talon has no telemetry.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/CRISTOP-bot/talon/internal/secrets"
)

// Kind classifies an event.
type Kind string

// Event kinds.
const (
	KindToolExec     Kind = "tool.exec"
	KindToolDeny     Kind = "tool.deny"
	KindApproval     Kind = "approval"
	KindNetwork      Kind = "network.request"
	KindNetworkBlock Kind = "network.block"
	KindSecretMask   Kind = "secret.redacted"
	KindInjection    Kind = "injection.detected"
	KindSandbox      Kind = "sandbox.applied"
	KindPolicyChange Kind = "policy.change"
	KindConsent      Kind = "legal.consent"
	KindDataRemoval  Kind = "data.removed"
	KindUpdate       Kind = "update.verify"
	KindSession      Kind = "session.start"
)

// Outcome records how an event ended.
type Outcome string

// Outcomes.
const (
	OutcomeAllowed Outcome = "allowed"
	OutcomeDenied  Outcome = "denied"
	OutcomeAsked   Outcome = "asked"
	OutcomeFailed  Outcome = "failed"
	OutcomeNeutral Outcome = "neutral"
)

// Level controls which events are recorded.
type Level int

// Verbosity levels.
const (
	LevelMinimal Level = iota // only refusals, blocks, consent and deletions
	LevelNormal               // adds tool executions and approvals
	LevelVerbose              // adds every event, including redactions
)

func (l Level) String() string {
	switch l {
	case LevelMinimal:
		return "minimal"
	case LevelVerbose:
		return "verbose"
	default:
		return "normal"
	}
}

// ParseLevel converts configuration text into a Level.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "minimal", "off":
		return LevelMinimal
	case "verbose", "debug":
		return LevelVerbose
	default:
		return LevelNormal
	}
}

// Event is one audit record.
type Event struct {
	Time    time.Time `json:"time"`
	Kind    Kind      `json:"kind"`
	Outcome Outcome   `json:"outcome"`
	// Action is what happened, e.g. "edit_file" or "POST /v1/messages".
	Action string `json:"action,omitempty"`
	// Target is what it happened to: a path, host or rule name.
	Target string `json:"target,omitempty"`
	// Reason explains a denial or a warning.
	Reason string `json:"reason,omitempty"`
	// Detail carries small, already-redacted extras.
	Detail map[string]any `json:"detail,omitempty"`
}

// String renders an event for the terminal.
func (e Event) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %-22s %-8s", e.Time.Format("15:04:05"), e.Kind, e.Outcome)
	if e.Action != "" {
		fmt.Fprintf(&b, " %s", e.Action)
	}
	if e.Target != "" {
		fmt.Fprintf(&b, " → %s", e.Target)
	}
	if e.Reason != "" {
		fmt.Fprintf(&b, " (%s)", e.Reason)
	}
	return b.String()
}

// Redacted returns the event with any secret-looking field masked. It is the
// form written to disk, so an event built from untrusted input cannot store a
// credential.
func (e Event) Redacted() Event {
	out := e
	out.Action = maskIfSecret(out.Action)
	out.Target = maskIfSecret(out.Target)
	out.Reason = maskIfSecret(out.Reason)
	if len(out.Detail) > 0 {
		cleaned := make(map[string]any, len(out.Detail))
		for k, v := range out.Detail {
			if s, ok := v.(string); ok {
				cleaned[k] = maskIfSecret(s)
				continue
			}
			cleaned[k] = v
		}
		out.Detail = cleaned
	}
	return out
}

func maskIfSecret(s string) string {
	if s == "" {
		return s
	}
	cleaned, findings := secrets.Redact(s, secrets.Bullets)
	if len(findings) > 0 {
		return cleaned
	}
	return s
}

// Log is an append-only local audit log.
type Log struct {
	mu      sync.Mutex
	path    string
	level   Level
	enabled bool
	file    *os.File
	// sessionID correlates events from one Talon run.
	sessionID string
}

// Options configures a Log.
type Options struct {
	// Path is the log file; empty disables persistence.
	Path string
	// Enabled turns the log on. A disabled log records nothing at all.
	Enabled bool
	// Level controls which events are recorded.
	Level Level
	// SessionID correlates events from the same run.
	SessionID string
}

// Open creates a log. A failure to open the file is returned so the caller can
// decide whether to continue without auditing; Talon warns instead of silently
// pretending to audit.
func Open(opts Options) (*Log, error) {
	l := &Log{path: opts.Path, level: opts.Level, enabled: opts.Enabled, sessionID: opts.SessionID}
	if !opts.Enabled || opts.Path == "" {
		return l, nil
	}
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o700); err != nil {
		return l, fmt.Errorf("cannot create the audit directory: %w", err)
	}
	f, err := os.OpenFile(opts.Path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return l, fmt.Errorf("cannot open the audit log: %w", err)
	}
	l.file = f
	return l, nil
}

// Enabled reports whether events are being persisted.
func (l *Log) Enabled() bool { return l != nil && l.enabled && l.file != nil }

// Path returns the log file path.
func (l *Log) Path() string { return l.path }

// Level returns the configured verbosity.
func (l *Log) Level() Level { return l.level }

// SessionID returns the current session identifier.
func (l *Log) SessionID() string { return l.sessionID }

// Close releases the file.
func (l *Log) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.file.Close()
	l.file = nil
	return err
}

// Record writes an event if the level allows it.
func (l *Log) Record(e Event) {
	if l == nil || !l.Enabled() {
		return
	}
	if !l.records(e) {
		return
	}
	e = e.Redacted()
	if e.Time.IsZero() {
		e.Time = time.Now()
	}
	e.Detail = withSession(e.Detail, l.sessionID)
	encoded, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.file.Write(append(encoded, '\n'))
}

func withSession(detail map[string]any, id string) map[string]any {
	if id == "" {
		return detail
	}
	out := make(map[string]any, len(detail)+1)
	for k, v := range detail {
		out[k] = v
	}
	out["session"] = id
	return out
}

// records decides whether an event passes the level filter.
func (l *Log) records(e Event) bool {
	switch l.level {
	case LevelVerbose:
		return true
	case LevelMinimal:
		switch e.Kind {
		case KindToolDeny, KindNetworkBlock, KindConsent, KindDataRemoval,
			KindInjection, KindSecretMask, KindUpdate, KindPolicyChange:
			return true
		default:
			return false
		}
	default:
		return true
	}
}

// Read returns the recorded events, newest last, up to limit (0 means all).
func Read(path string, limit int) ([]Event, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var events []Event
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if e.Kind == "" {
			// A syntactically valid but empty record carries nothing usable.
			continue
		}
		events = append(events, e)
	}
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	return events, nil
}

// Count returns how many events are recorded.
func Count(path string) (int, error) {
	events, err := Read(path, 0)
	return len(events), err
}

// Size returns the log size in bytes.
func Size(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

// Remove deletes the log file.
func Remove(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// Convenience recorders used across the codebase. They keep event names
// consistent so `talon audit` output is greppable.

func (l *Log) ToolAllowed(tool, target string) {
	l.Record(Event{Kind: KindToolExec, Outcome: OutcomeAllowed, Action: tool, Target: target})
}

func (l *Log) ToolDenied(tool, target, reason string) {
	l.Record(Event{Kind: KindToolDeny, Outcome: OutcomeDenied, Action: tool, Target: target, Reason: reason})
}

func (l *Log) ToolFailed(tool, target, reason string) {
	l.Record(Event{Kind: KindToolExec, Outcome: OutcomeFailed, Action: tool, Target: target, Reason: reason})
}

func (l *Log) Approval(tool string, always bool) {
	outcome := OutcomeAllowed
	detail := map[string]any{}
	if always {
		detail["scope"] = "session"
	}
	l.Record(Event{Kind: KindApproval, Outcome: outcome, Action: tool, Detail: detail})
}

func (l *Log) ApprovalDenied(tool, reason string) {
	l.Record(Event{Kind: KindApproval, Outcome: OutcomeDenied, Action: tool, Reason: reason})
}

func (l *Log) Network(host, url string) {
	l.Record(Event{Kind: KindNetwork, Outcome: OutcomeAllowed, Action: host, Target: url})
}

func (l *Log) NetworkBlocked(host, reason string) {
	l.Record(Event{Kind: KindNetworkBlock, Outcome: OutcomeDenied, Action: host, Reason: reason})
}

func (l *Log) SecretRedacted(kind, where string) {
	l.Record(Event{Kind: KindSecretMask, Outcome: OutcomeNeutral, Action: kind, Target: where})
}

func (l *Log) Injection(source, summary string) {
	l.Record(Event{Kind: KindInjection, Outcome: OutcomeNeutral, Action: source, Target: summary})
}

func (l *Log) Sandbox(detail string) {
	l.Record(Event{Kind: KindSandbox, Outcome: OutcomeNeutral, Action: detail})
}

func (l *Log) PolicyChange(what, from, to string) {
	l.Record(Event{Kind: KindPolicyChange, Outcome: OutcomeNeutral, Action: what,
		Detail: map[string]any{"from": from, "to": to}})
}

func (l *Log) Consent(version, what string) {
	l.Record(Event{Kind: KindConsent, Outcome: OutcomeNeutral, Action: what, Target: version})
}

func (l *Log) DataRemoved(what string) {
	l.Record(Event{Kind: KindDataRemoval, Outcome: OutcomeNeutral, Action: what})
}

func (l *Log) UpdateVerified(target, detail string) {
	l.Record(Event{Kind: KindUpdate, Outcome: OutcomeAllowed, Action: target, Target: detail})
}

func (l *Log) SessionStarted(provider, model string) {
	l.Record(Event{Kind: KindSession, Outcome: OutcomeNeutral, Action: provider, Target: model})
}
