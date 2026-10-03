// Package sensitive decides what Talon may read into a model prompt.
//
// Two independent gates:
//
//  1. Path classification. Files that are credential material by name (.env,
//     id_rsa, *.pem, .npmrc, cloud service-account keys…) are blocked by
//     default, even if the user asked for them.
//  2. Content scanning. Anything that is read is scanned for credential
//     patterns; findings are either blocked, masked or warned about, according
//     to policy.
//
// The gate lives between the filesystem tools and the model, so it applies to
// every tool that produces content, including shell output.
package sensitive

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/secrets"
)

// Action is what to do with sensitive content.
type Action string

// Actions, from least to most permissive.
const (
	// ActionBlock refuses the read or send entirely.
	ActionBlock Action = "block"
	// ActionMask replaces the secrets and continues.
	ActionMask Action = "mask"
	// ActionWarn allows the content and tells the user what was found.
	ActionWarn Action = "warn"
	// ActionAllow allows everything (explicit opt-in only).
	ActionAllow Action = "allow"
)

// ParseAction converts configuration text into an Action.
func ParseAction(s string) (Action, error) {
	switch Action(strings.ToLower(strings.TrimSpace(s))) {
	case ActionBlock:
		return ActionBlock, nil
	case ActionMask:
		return ActionMask, nil
	case ActionWarn:
		return ActionWarn, nil
	case ActionAllow:
		return ActionAllow, nil
	}
	return "", fmt.Errorf("unknown sensitive-data action %q (use block, mask, warn or allow)", s)
}

// Policy configures the gates.
type Policy struct {
	// SensitiveFiles is the action for credential material detected by name.
	SensitiveFiles Action
	// ContentFindings is the action for credential patterns in content.
	ContentFindings Action
	// AllowedPaths re-enables specific sensitive files, by path fragment.
	AllowedPaths []string
	// MaxFileBytes blocks reads of files above this size.
	MaxFileBytes int64
	// InPrivacyMode refuses everything not explicitly allowed.
	InPrivacyMode bool
}

// DefaultPolicy returns the safe default: block credential files by name, mask
// credentials found in content.
func DefaultPolicy() Policy {
	return Policy{
		SensitiveFiles:  ActionBlock,
		ContentFindings: ActionMask,
		MaxFileBytes:    512 * 1024,
	}
}

// Decision is the outcome of a gate check.
type Decision struct {
	Allowed bool
	// Action is what was applied.
	Action Action
	// Reason explains a refusal or a warning in one sentence.
	Reason string
	// Findings lists the credential kinds detected.
	Findings []secrets.Finding
	// Content is the (possibly masked) content that may be used.
	Content string
}

// Redacted reports whether secrets were replaced.
func (d Decision) Redacted() bool { return d.Action == ActionMask }

// CheckPath evaluates a file path against the policy.
func (p Policy) CheckPath(path string) Decision {
	clean := filepath.ToSlash(path)
	if p.pathAllowed(clean) {
		return Decision{Allowed: true, Action: ActionAllow,
			Reason: "explicitly allowed by security.allow_sensitive_paths"}
	}
	if !secrets.IsSensitivePath(clean) {
		if p.MaxFileBytes > 0 {
			// Size is checked by the caller; nothing to do here.
			_ = p.MaxFileBytes
		}
		return Decision{Allowed: true, Action: ActionAllow}
	}
	switch p.SensitiveFiles {
	case ActionAllow:
		return Decision{Allowed: true, Action: ActionAllow,
			Reason: "security.sensitive_files = allow"}
	case ActionMask:
		return Decision{Allowed: false, Action: ActionMask,
			Reason: fmt.Sprintf("%s is credential material; masking is not implemented for whole files, "+
				"copy out the value you need and pass it explicitly", clean)}
	default:
		return Decision{Allowed: false, Action: ActionBlock,
			Reason: fmt.Sprintf("%s looks like credential material and is blocked from being sent to the model; "+
				"add it to security.allow_sensitive_paths if you really need it", clean)}
	}
}

func (p Policy) pathAllowed(clean string) bool {
	for _, allow := range p.AllowedPaths {
		a := strings.TrimSpace(allow)
		if a == "" {
			continue
		}
		if filepath.IsAbs(a) {
			if clean == filepath.ToSlash(a) {
				return true
			}
			continue
		}
		if strings.Contains(clean, a) || strings.Contains(clean, strings.TrimPrefix(a, "./")) {
			return true
		}
	}
	return false
}

// CheckContent scans content for credentials and applies the policy. The
// returned content is safe to place in a prompt.
func (p Policy) CheckContent(source, content string) Decision {
	findings := secrets.ScanLines(content)
	if len(findings) == 0 {
		// Whole-file scan catches multi-line secrets (private keys) that the
		// line scanner would miss.
		if extra := secrets.Detect(content); len(extra) > 0 {
			findings = append(findings, extra...)
		}
	}
	if len(findings) == 0 {
		return Decision{Allowed: true, Action: ActionAllow, Content: content}
	}
	// Deduplicate by position.
	seen := map[string]bool{}
	unique := findings[:0]
	for _, f := range findings {
		key := fmt.Sprintf("%s:%d", f.Kind, f.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, f)
	}
	findings = unique

	switch p.ContentFindings {
	case ActionAllow:
		return Decision{Allowed: true, Action: ActionAllow, Findings: findings, Content: content,
			Reason: fmt.Sprintf("%d credential(s) present in %s; security allows sending them", len(findings), source)}
	case ActionWarn:
		return Decision{Allowed: true, Action: ActionWarn, Findings: findings, Content: content,
			Reason: fmt.Sprintf("%d credential(s) present in %s and will be sent as-is", len(findings), source)}
	case ActionBlock:
		return Decision{Allowed: false, Action: ActionBlock, Findings: findings,
			Reason: fmt.Sprintf("%s contains %d credential(s) and will not be sent to the model",
				source, len(findings))}
	default:
		masked, maskedFindings := secrets.Redact(content, secrets.Placeholder)
		return Decision{
			Allowed:  true,
			Action:   ActionMask,
			Findings: append(findings, maskedFindings...),
			Content:  masked,
			Reason: fmt.Sprintf("%d credential(s) in %s were replaced with placeholders before sending",
				len(findings), source),
		}
	}
}

// Summary renders findings as a short, user-facing list.
func Summary(findings []secrets.Finding) string {
	if len(findings) == 0 {
		return ""
	}
	kinds := map[string]int{}
	for _, f := range findings {
		kinds[f.Kind]++
	}
	keys := make([]string, 0, len(kinds))
	for k := range kinds {
		keys = append(keys, k)
	}
	sortStrings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, kinds[k]))
	}
	return strings.Join(parts, ", ")
}

// CheckAndApply runs the content gate, returning a decision the caller can act
// on without further parsing.
func (p Policy) CheckAndApply(source, content string) Decision {
	d := p.CheckContent(source, content)
	if d.Allowed && d.Action == ActionMask {
		return d
	}
	if !d.Allowed {
		// A blocked file still gets its secrets masked if the caller chooses to
		// show a preview; the decision itself stays a refusal.
		masked, _ := secrets.Redact(content, secrets.Bullets)
		d.Content = masked
	}
	return d
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
