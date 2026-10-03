package repl

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/audit"
	"github.com/CRISTOP-bot/talon/internal/injection"
	"github.com/CRISTOP-bot/talon/internal/legal"
	"github.com/CRISTOP-bot/talon/internal/llm"
	"github.com/CRISTOP-bot/talon/internal/perm"
	"github.com/CRISTOP-bot/talon/internal/privacy"
	"github.com/CRISTOP-bot/talon/internal/secrets"
	"github.com/CRISTOP-bot/talon/internal/secure"
	"github.com/CRISTOP-bot/talon/internal/shell"
	"github.com/CRISTOP-bot/talon/internal/tools"
)

// buildSecurity assembles the security posture and points every component at it.
func (r *REPL) buildSecurity(ctx context.Context) error {
	posture, err := secure.New(secure.Options{
		Config:         r.cfg,
		Workspace:      r.opts.Workspace,
		PrivacyMode:    r.opts.PrivacyMode,
		ConfirmNetwork: r.opts.ConfirmNetwork,
		SessionID:      r.sessionID(),
	})
	if err != nil {
		return err
	}
	r.posture = posture
	r.opts.Log = posture.Log

	ledger, lerr := legal.OpenLedger(privacy.LegalPath())
	if lerr != nil {
		if r.opts.Log != nil {
			r.opts.Log.Warnf("legal ledger unavailable: %v", lerr)
		}
	} else {
		r.legal = ledger
	}

	// Privacy mode must also switch off persistence.
	if posture.Privacy.Enabled {
		r.cfg.Sessions.Autosave = false
		r.cfg.Sessions.Enabled = false
		r.cfg.Memory.Enabled = false
		r.cfg.Logging.Level = "off"
		r.cfg.Security.AuditEnabled = false
		r.cfg.Plugins.Enabled = false
	}

	// The provider must go through the network policy.
	r.httpClient = posture.Network.HTTPClient(r.cfg.Model.TimeoutSeconds)

	for _, note := range posture.Notes {
		if r.opts.Log != nil {
			r.opts.Log.Warnf("security: %s", note)
		}
	}
	if r.posture.Audit != nil {
		r.posture.Audit.SessionStarted(r.cfg.Model.Provider, r.cfg.Model.Name)
		r.posture.Audit.PolicyChange("permission-level", "startup", r.cfg.Permissions.Level)
		if posture.SandboxEnabled {
			r.posture.Audit.Sandbox("landlock=" + fmt.Sprint(posture.SandboxStatus.Available) +
				" abi=" + fmt.Sprint(posture.SandboxStatus.LandlockABI))
		}
	}

	// Command execution follows the sandbox policy unless the user refused it.
	if allowed, reason := posture.CommandAllowed(); !allowed {
		r.pr.Muted("commands are disabled: " + reason)
	}
	return nil
}

// gates builds the security gates handed to every tool.
func (r *REPL) gates() tools.Gates {
	p := r.posture
	g := tools.Gates{
		Audit:       p.Audit,
		PrivacyMode: p.Privacy.Enabled,
	}
	if p == nil {
		return g
	}
	g.SensitivePath = func(path string) error {
		d := p.Sensitive.CheckPath(path)
		if d.Allowed {
			return nil
		}
		if p.Audit != nil {
			p.Audit.ToolDenied("sensitive-file", path, d.Reason)
		}
		return fmt.Errorf("%s", d.Reason)
	}
	g.SensitiveContent = func(source, content string) (string, string, error) {
		d := p.Sensitive.CheckContent(source, content)
		if !d.Allowed {
			// Refusing is a hard stop: the caller must not receive the content.
			if p.Audit != nil {
				p.Audit.ToolDenied("sensitive-content", source, d.Reason)
			}
			return "", "", fmt.Errorf("%s", d.Reason)
		}
		if len(d.Findings) > 0 && p.Audit != nil {
			p.Audit.SecretRedacted(sensitiveSummary(d.Findings), source)
		}
		return d.Content, d.Reason, nil
	}
	g.WrapUntrusted = func(source, content string) string {
		return injection.Wrap(content, injection.Provenance{
			Kind:      injection.KindFile,
			Source:    source,
			Untrusted: true,
		})
	}
	g.ScanInjection = func(source, content string) string {
		findings := injection.Scan(content)
		if len(findings) == 0 {
			return ""
		}
		if p.Audit != nil {
			p.Audit.Injection(source, injection.Summary(findings))
		}
		return injection.Notice(findings)
	}
	g.SandboxPolicy = func() *shell.SandboxPolicy {
		if !p.SandboxEnabled {
			return nil
		}
		policy := p.SandboxPolicy(r.opts.Workspace, r.childEnv())
		return &policy
	}
	g.EnvSanitizer = func(env []string) []string {
		return secrets.FilterEnv(env, r.allowedChildEnv(), false)
	}
	return g
}

// childEnv builds the environment child processes receive. Credentials are
// removed unless a command genuinely needs one and the user allowed it.
func (r *REPL) childEnv() []string {
	return r.gates().EnvSanitizer(os.Environ())
}

// allowedChildEnv lists the credentials a command may see. It is empty by
// default: passing an API key to a subprocess is almost never needed and is a
// common way secrets leak into build logs.
func (r *REPL) allowedChildEnv() []string {
	var allow []string
	if r.cfg.Security.AllowHTTP || r.cfg.Security.AllowLoopback {
		// Local model servers sometimes read a key from the environment; the user
		// opted in by allowing those hosts.
		allow = append(allow, r.cfg.Model.APIKeyEnv)
	}
	return allow
}

func sensitiveSummary(findings []secrets.Finding) string {
	kinds := map[string]bool{}
	for _, f := range findings {
		kinds[f.Kind] = true
	}
	names := make([]string, 0, len(kinds))
	for k := range kinds {
		names = append(names, k)
	}
	return strings.Join(names, ",")
}

// Posture exposes the active security configuration.
func (r *REPL) Posture() *secure.Posture { return r.posture }

// AuditLog exposes the audit log.
func (r *REPL) AuditLog() *audit.Log {
	if r.posture == nil {
		return nil
	}
	return r.posture.Audit
}

// providerOptions builds the provider options, including the policed client.
func (r *REPL) providerOptions() llm.Options {
	return llm.Options{
		APIKey:          r.cfg.APIKeyValue(),
		BaseURL:         r.cfg.Model.BaseURL,
		Model:           r.cfg.Model.Name,
		HTTPClient:      r.httpClient,
		Timeout:         llm.ResolveTimeout(r.cfg.Model.TimeoutSeconds),
		MaxRetries:      r.cfg.Model.MaxRetries,
		Headers:         r.cfg.Model.ExtraHeaders,
		MaxOutputTokens: r.cfg.Model.MaxTokens,
	}
}

// checkConsent warns when the Terms in force have not been accepted. It warns
// rather than blocks, because a session must remain usable for someone who only
// wants to read what Talon does.
func (r *REPL) checkConsent() {
	blocked, message := r.termsGate()
	if !blocked {
		return
	}
	r.pr.Blank()
	r.pr.Warn("the Terms of Use changed and have not been accepted yet")
	r.pr.Muted(message)
	r.pr.Muted("read them with /terms and record acceptance with /terms accept")
	r.pr.Muted("or start Talon with --accept-terms")
	r.pr.Blank()
}

// termsGate reports whether the current Terms version has been accepted.
func (r *REPL) termsGate() (blocked bool, message string) {
	if !r.cfg.Security.RequireTerms {
		return false, ""
	}
	status, err := r.legal.Check("terms")
	if err != nil {
		return false, ""
	}
	if !status.NeedsConsent {
		return false, ""
	}
	return true, status.Reason + "\n" + status.Changes
}

// PermissionFor returns the level actually in force. Privacy mode caps
// autonomy at "confirm": a private run must never act unattended.
func (r *REPL) permissionFor(level string) perm.Level {
	l := perm.Level(level)
	if r.posture != nil && r.posture.Privacy.Enabled && autonomyRank(l) > autonomyRank(perm.Confirm) {
		return perm.Confirm
	}
	return l
}

// autonomyRank orders permission levels from least to most autonomous.
func autonomyRank(l perm.Level) int {
	switch l {
	case perm.ReadOnly:
		return 0
	case perm.Safe:
		return 1
	case perm.Confirm:
		return 2
	case perm.FullAccess:
		return 3
	}
	return 0
}
