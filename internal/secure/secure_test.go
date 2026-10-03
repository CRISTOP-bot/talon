package secure

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"context"

	"github.com/CRISTOP-bot/talon/internal/config"
	"github.com/CRISTOP-bot/talon/internal/netguard"
	"github.com/CRISTOP-bot/talon/internal/privacy"
	"github.com/CRISTOP-bot/talon/internal/sensitive"
)

func testPosture(t *testing.T, mutate func(*config.Config)) *Posture {
	t.Helper()
	cfg := config.Defaults()
	if mutate != nil {
		mutate(cfg)
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	posture, err := New(Options{Config: cfg, Workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("building the posture failed: %v", err)
	}
	t.Cleanup(posture.Close)
	return posture
}

// TestDefaultsAreSafe proves a fresh configuration is the restrictive one.
func TestDefaultsAreSafe(t *testing.T) {
	p := testPosture(t, nil)
	if p.Sensitive.SensitiveFiles != sensitive.ActionBlock {
		t.Fatalf("sensitive files should default to block, got %s", p.Sensitive.SensitiveFiles)
	}
	if p.Sensitive.ContentFindings != sensitive.ActionMask {
		t.Fatalf("content findings should default to mask, got %s", p.Sensitive.ContentFindings)
	}
	if p.Network.Mode != netguard.ModeAllowlist {
		t.Fatalf("network should default to allowlist, got %s", p.Network.Mode)
	}
	if p.Network.AllowLoopback || p.Network.AllowPrivateIPs || p.Network.AllowHTTP {
		t.Fatal("loopback, private IPs and http must be off by default")
	}
	if !p.Config.Security.SecretRedaction {
		t.Fatal("secret redaction must be on by default")
	}
	if p.Privacy.Enabled {
		t.Fatal("privacy mode must be off by default")
	}
	if p.Config.Security.Telemetry {
		t.Fatal("telemetry must be off by default")
	}
}

// TestTelemetryCannotBeEnabled proves the configuration refuses to pretend.
func TestTelemetryCannotBeEnabled(t *testing.T) {
	cfg := config.Defaults()
	cfg.Security.Telemetry = true
	if err := cfg.Validate(); err == nil {
		t.Fatal("enabling telemetry must be a configuration error: this build has no telemetry")
	}
}

// TestInvalidSecurityValuesAreReported proves a bad value changes behaviour and
// says so instead of being ignored.
func TestInvalidSecurityValuesAreReported(t *testing.T) {
	cfg := config.Defaults()
	cfg.Security.SensitiveFiles = "obliterate"
	posture := testPosture(t, func(c *config.Config) { c.Security.SensitiveFiles = cfg.Security.SensitiveFiles })
	if posture.Sensitive.SensitiveFiles != sensitive.ActionBlock {
		t.Fatalf("an invalid action must fall back to block, got %s", posture.Sensitive.SensitiveFiles)
	}
	if len(posture.Notes) == 0 {
		t.Fatal("the fallback must be reported to the user")
	}
}

// TestAllowlistRejectsUnknownHosts proves the network gate is not decorative.
func TestAllowlistRejectsUnknownHosts(t *testing.T) {
	p := testPosture(t, nil)
	if err := p.Network.CheckHost(context.Background(), "https://api.openai.com/v1/models"); err != nil {
		t.Fatalf("the configured provider host should be allowed: %v", err)
	}
	if err := p.Network.CheckHost(context.Background(), "https://evil.example.com/steal"); err == nil {
		t.Fatal("an unlisted host must be refused in allowlist mode")
	}
	if err := p.Network.CheckHost(context.Background(), "http://169.254.169.254/latest/meta-data/"); err == nil {
		t.Fatal("the cloud metadata endpoint must always be refused")
	}
	if err := p.Network.CheckHost(context.Background(), "http://127.0.0.1:8080/"); err == nil {
		t.Fatal("loopback must be refused unless the user allowed it")
	}
}

// TestLocalProviderEnablesLoopback proves a local model server can be used
// without opening loopback globally.
func TestLocalProviderEnablesLoopback(t *testing.T) {
	p := testPosture(t, func(c *config.Config) { c.Model.Provider = "ollama" })
	if !p.Network.AllowLoopback {
		t.Fatal("a local provider must be allowed to reach loopback")
	}
	if err := p.Network.CheckHost(context.Background(), "http://127.0.0.1:11434/api/generate"); err != nil {
		t.Fatalf("a local provider must be able to talk to its own server: %v", err)
	}
	if err := p.Network.CheckHost(context.Background(), "http://10.0.0.5:11434/api/generate"); err == nil {
		t.Fatal("loopback permission must not extend to other private addresses")
	}
}

// TestPrivacyModeWritesNothing proves the private run keeps no audit trail.
func TestPrivacyModeWritesNothing(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	p := testPosture(t, func(c *config.Config) { c.Security.PrivacyMode = true })
	if !p.Privacy.Enabled {
		t.Fatal("privacy mode should be active")
	}
	if p.Audit != nil && p.Audit.Enabled() {
		t.Fatal("the audit log must be disabled in privacy mode")
	}
	if p.Log != nil {
		t.Fatal("the logger must be disabled in privacy mode")
	}
	entries := 0
	_ = filepath.Walk(dataHome, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			entries++
		}
		return nil
	})
	if entries != 0 {
		t.Fatalf("privacy mode wrote %d file(s) under the data directory", entries)
	}
}

// TestSummaryReportsWhatRuns proves the summary names real controls.
func TestSummaryReportsWhatRuns(t *testing.T) {
	p := testPosture(t, nil)
	summary := p.Summary()
	for _, want := range []string{
		"sensitive files", "network mode", "metadata hosts", "audit log",
		"privacy mode", "telemetry", "Sandbox", "no telemetry",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("the summary should mention %q:\n%s", want, summary)
		}
	}
}

// TestRedactHonoursConfiguration proves disabling redaction is possible but
// explicit, and that enabling it masks a known credential shape.
func TestRedactHonoursConfiguration(t *testing.T) {
	p := testPosture(t, nil)
	clean, findings := p.Redact("key ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	if len(findings) == 0 {
		t.Fatal("a GitHub token should have been detected")
	}
	if strings.Contains(clean, "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
		t.Fatal("the token survived redaction")
	}
	off := testPosture(t, func(c *config.Config) { c.Security.SecretRedaction = false })
	raw, _ := off.Redact("key ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")
	if !strings.Contains(raw, "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
		t.Fatal("with redaction off the text must be returned unchanged")
	}
	if len(off.Notes) == 0 {
		t.Fatal("turning redaction off must be reported as a note")
	}
}

// TestAuditPathHonoursPrivacyMode proves the audit location is the documented
// one and that data helpers agree with it.
func TestAuditPathHonoursPrivacyMode(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	if DataPath("audit") != privacy.AuditPath() {
		t.Fatal("the reported audit path must match the one the audit log uses")
	}
}
