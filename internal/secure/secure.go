// Package secure assembles Talon's security posture from configuration and
// exposes it to the rest of the program.
//
// It is the single place that decides:
//
//   - which files may be read into a prompt (internal/sensitive)
//   - where the network may go (internal/netguard)
//   - how commands are confined (internal/sandbox)
//   - which secrets are masked on the way out (internal/secrets)
//   - what is written to the audit log (internal/audit)
//
// Everything the model can influence flows through one of those gates, and none
// of them reads its instructions to decide what is allowed.
package secure

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/talon-cli/talon/internal/audit"
	"github.com/talon-cli/talon/internal/config"
	"github.com/talon-cli/talon/internal/logger"
	"github.com/talon-cli/talon/internal/netguard"
	"github.com/talon-cli/talon/internal/paths"
	"github.com/talon-cli/talon/internal/privacy"
	"github.com/talon-cli/talon/internal/sandbox"
	"github.com/talon-cli/talon/internal/secrets"
	"github.com/talon-cli/talon/internal/sensitive"
)

// Posture is the assembled security configuration for one run.
type Posture struct {
	Config *config.Config
	// Sensitive decides what may be read and sent.
	Sensitive sensitive.Policy
	// Network decides where requests may go.
	Network *netguard.Policy
	// Sandbox decides how child processes are confined.
	SandboxEnabled bool
	// SandboxStatus reports what the kernel can actually enforce.
	SandboxStatus sandbox.Status
	// Audit records security decisions locally.
	Audit *audit.Log
	// Privacy is the active privacy mode.
	Privacy privacy.Mode
	// Log is the redacting logger; nil in privacy mode.
	Log *logger.Logger
	// Register marks the configured API key as a secret.
	Register func(values ...string)
	// Notes lists configuration problems that changed behaviour.
	Notes []string
}

// Options configures a Posture.
type Options struct {
	Config *config.Config
	// Workspace is the project root, used for the sandbox write root.
	Workspace string
	// PrivacyMode forces privacy mode on regardless of configuration.
	PrivacyMode bool
	// ConfirmNetwork is called when a host outside the allow list needs a
	// decision. When nil, such requests are refused.
	ConfirmNetwork func(host string) error
	// SessionID correlates audit records.
	SessionID string
}

// New assembles the posture. It never fails: a broken security setting must not
// stop Talon from starting, but the affected control is disabled and the reason
// is reported through Notes.
func New(opts Options) (*Posture, error) {
	cfg := opts.Config
	if cfg == nil {
		cfg = config.Defaults()
	}
	p := &Posture{Config: cfg}
	var notes []string

	// --- sensitive data -----------------------------------------------------
	p.Sensitive = sensitive.DefaultPolicy()
	p.Sensitive.MaxFileBytes = cfg.Security.MaxFileBytes
	p.Sensitive.AllowedPaths = cfg.Security.AllowSensitivePaths
	fileAction, err := sensitive.ParseAction(cfg.Security.SensitiveFiles)
	if err != nil {
		notes = append(notes, err.Error()+"; falling back to block")
		fileAction = sensitive.ActionBlock
	}
	contentAction, err := sensitive.ParseAction(cfg.Security.ContentFindings)
	if err != nil {
		notes = append(notes, err.Error()+"; falling back to mask")
		contentAction = sensitive.ActionMask
	}
	p.Sensitive.SensitiveFiles = fileAction
	p.Sensitive.ContentFindings = contentAction
	p.Sensitive.InPrivacyMode = cfg.Security.PrivacyMode || opts.PrivacyMode

	// --- network ------------------------------------------------------------
	p.Network = netguard.DefaultPolicy()
	p.Network.Mode = netguard.Mode(cfg.Security.NetworkMode)
	switch p.Network.Mode {
	case netguard.ModeAllowlist, netguard.ModeAsk, netguard.ModeOff:
	default:
		notes = append(notes, "security.network_mode is invalid; falling back to allowlist")
		p.Network.Mode = netguard.ModeAllowlist
	}
	// A local model server is normally plain http on loopback; permitting that
	// for local providers keeps the default safe for the public internet.
	local := netguard.IsLocalProviderHost(cfg.Model.Provider)
	p.Network.AllowHTTP = cfg.Security.AllowHTTP || local
	p.Network.AllowLoopback = cfg.Security.AllowLoopback || netguard.IsLocalProviderHost(cfg.Model.Provider)
	p.Network.AllowPrivateIPs = cfg.Security.AllowPrivateIPs || netguard.IsLocalProviderHost(cfg.Model.Provider)
	p.Network.Confirm = opts.ConfirmNetwork
	if !cfg.Security.NetworkConfirmation {
		p.Network.Confirm = nil
		notes = append(notes, "network confirmation disabled: hosts outside the allow list are refused")
	}
	for _, host := range netguard.ProviderHosts(cfg.Model.Provider, cfg.Model.BaseURL) {
		p.Network.Allow(host)
	}
	for _, host := range cfg.Security.AllowedDomains {
		p.Network.Allow(host)
	}
	if !cfg.Security.SecretRedaction {
		notes = append(notes, "secret redaction disabled: credentials may appear in logs and prompts")
	}

	// --- sandbox ------------------------------------------------------------
	p.SandboxStatus = sandbox.Detect()
	p.SandboxEnabled = cfg.Security.Sandbox
	if p.SandboxEnabled && !p.SandboxStatus.Available {
		if cfg.Security.SandboxRequired {
			notes = append(notes, "sandbox required but unavailable: shell commands will be refused")
		} else {
			notes = append(notes, "no kernel sandbox available: commands run confined only by Talon's policy")
		}
	}

	// --- privacy and audit --------------------------------------------------
	p.Privacy = privacy.Mode{Enabled: cfg.Security.PrivacyMode || opts.PrivacyMode}
	if opts.PrivacyMode {
		p.Privacy.Reason = "--privacy"
	}
	auditPath := cfg.Security.AuditPath
	if auditPath == "" {
		auditPath = privacy.AuditPath()
	}
	auditEnabled := cfg.Security.AuditEnabled && !p.Privacy.Enabled
	level := audit.ParseLevel(cfg.Security.AuditLevel)
	if cfg.Security.AuditLevel == "off" {
		level = audit.LevelMinimal
		auditEnabled = false
	}
	al, aerr := audit.Open(audit.Options{
		Path: auditPath, Enabled: auditEnabled, Level: level, SessionID: opts.SessionID,
	})
	if aerr != nil {
		notes = append(notes, "audit log unavailable: "+aerr.Error())
	}
	p.Audit = al

	// Wire audit decisions into the network policy so every request is recorded.
	p.Network.OnDecision = func(d netguard.Decision) {
		if !al.Enabled() {
			return
		}
		if d.Allowed {
			al.Network(d.Host, d.URL)
			return
		}
		al.NetworkBlocked(d.Host, d.Reason)
	}

	// --- logger -------------------------------------------------------------
	if !p.Privacy.Enabled && cfg.Logging.Level != "off" {
		p.Log = logger.New(logger.Options{
			Level:  logger.ParseLevel(cfg.Logging.Level),
			File:   cfg.Logging.File,
			Stderr: nil, // the REPL prints user-facing errors itself
			Redact: cfg.Security.SecretRedaction,
		})
	}
	p.Register = func(values ...string) {
		if p.Log != nil {
			p.Log.Secret(values...)
		}
		logger.RegisterSecret(values...)
	}
	if key := cfg.APIKeyValue(); key != "" {
		p.Register(key)
	}
	p.Notes = notes
	return p, nil
}

// SandboxPolicy builds the confinement policy for a command run in workspace.
func (p *Posture) SandboxPolicy(workspace string, env []string) sandbox.Policy {
	if !p.SandboxEnabled {
		return sandbox.Policy{}
	}
	return sandbox.DefaultPolicy(workspace, env)
}

// CommandAllowed reports whether shell commands may run at all, given the
// sandbox policy and the user's configuration.
func (p *Posture) CommandAllowed() (bool, string) {
	if p.SandboxEnabled && !p.SandboxStatus.Available && p.Config.Security.SandboxRequired {
		return false, "security.sandbox_required is set but no kernel sandbox is available on this machine"
	}
	if p.Privacy.Enabled && p.Config.Permissions.Level == "read-only" {
		return false, "read-only mode does not permit commands"
	}
	return true, ""
}

// Summary renders the posture for `talon security`.
func (p *Posture) Summary() string {
	var b strings.Builder
	b.WriteString("Security posture\n\n")
	rows := [][2]string{
		{"command confirmation", boolLabel(p.Config.Security.CommandConfirmation)},
		{"sensitive files", string(p.Sensitive.SensitiveFiles)},
		{"credentials in content", string(p.Sensitive.ContentFindings)},
		{"secret redaction", boolLabel(p.Config.Security.SecretRedaction)},
		{"network mode", string(p.Network.Mode)},
		{"network confirmation", boolLabel(p.Config.Security.NetworkConfirmation)},
		{"allowed hosts", listOrNone(p.Network.AllowedHosts)},
		{"http permitted", boolLabel(p.Network.AllowHTTP)},
		{"loopback permitted", boolLabel(p.Network.AllowLoopback)},
		{"private IPs permitted", boolLabel(p.Network.AllowPrivateIPs)},
		{"cloud metadata hosts", "always blocked"},
		{"audit log", auditLabel(p.Audit)},
		{"privacy mode", boolLabel(p.Privacy.Enabled)},
		{"terms required", boolLabel(p.Config.Security.RequireTerms)},
		{"telemetry", "disabled (this build has no telemetry)"},
	}
	width := 0
	for _, r := range rows {
		if len(r[0]) > width {
			width = len(r[0])
		}
	}
	for _, r := range rows {
		fmt.Fprintf(&b, "  %-*s  %s\n", width, r[0], r[1])
	}
	b.WriteString("\nSandbox\n  ")
	b.WriteString(strings.ReplaceAll(p.SandboxStatus.String(), "\n", "\n  "))
	if len(p.Notes) > 0 {
		b.WriteString("\nNotes\n")
		for _, n := range p.Notes {
			fmt.Fprintf(&b, "  ! %s\n", n)
		}
	}
	return b.String()
}

func boolLabel(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

func auditLabel(a *audit.Log) string {
	if a == nil || !a.Enabled() {
		return "disabled"
	}
	return fmt.Sprintf("enabled (%s) → %s", a.Level(), a.Path())
}

func listOrNone(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	sorted := append([]string(nil), items...)
	sort.Strings(sorted)
	return strings.Join(sorted, ", ")
}

// Redact scrubs text according to the configured policy. When redaction is off
// it returns the input unchanged, because the user asked for that explicitly.
func (p *Posture) Redact(text string, extra ...string) (string, []secrets.Finding) {
	if !p.Config.Security.SecretRedaction {
		return text, nil
	}
	return secrets.Redact(text, secrets.Bullets, extra...)
}

// Close releases resources owned by the posture.
func (p *Posture) Close() {
	if p.Log != nil {
		p.Log.Close()
	}
	if p.Audit != nil {
		_ = p.Audit.Close()
	}
}

// DataPath returns the resolved path of a stored-data category.
func DataPath(kind string) string {
	switch kind {
	case "audit":
		return privacy.AuditPath()
	case "legal":
		return privacy.LegalPath()
	default:
		return filepath.Join(paths.Data(), kind)
	}
}
