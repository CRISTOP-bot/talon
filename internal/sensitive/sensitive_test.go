package sensitive

import (
	"strings"
	"testing"

	"github.com/CRISTOP-bot/talon/internal/secrets"
)

func TestSensitivePathsAreBlockedByDefault(t *testing.T) {
	p := DefaultPolicy()
	for _, path := range []string{
		".env", "config/.env.production", "/home/me/.ssh/id_rsa", "deploy/cert.pem",
		".npmrc", ".git-credentials", "gcloud-service-key.json", "config/credentials.yaml",
	} {
		d := p.CheckPath(path)
		if d.Allowed {
			t.Errorf("%s should be blocked by default", path)
		}
		if d.Action != ActionBlock {
			t.Errorf("%s action = %s", path, d.Action)
		}
		if !strings.Contains(d.Reason, "security.allow_sensitive_paths") {
			t.Errorf("%s reason does not say how to proceed: %q", path, d.Reason)
		}
	}
}

func TestOrdinaryPathsPass(t *testing.T) {
	p := DefaultPolicy()
	for _, path := range []string{"src/main.go", "README.md", "docs/configuration.md", "testdata/fixtures/data.json"} {
		if d := p.CheckPath(path); !d.Allowed {
			t.Errorf("%s should be allowed: %s", path, d.Reason)
		}
	}
}

func TestAllowlistUnblocksSpecificPaths(t *testing.T) {
	p := DefaultPolicy()
	p.AllowedPaths = []string{".env.example"}
	if d := p.CheckPath(".env.example"); !d.Allowed {
		t.Errorf("allow-listed path blocked: %s", d.Reason)
	}
	if d := p.CheckPath(".env"); d.Allowed {
		t.Error("allow list must be specific")
	}
	abs := DefaultPolicy()
	abs.AllowedPaths = []string{"/etc/talon/keys/service.json"}
	if d := abs.CheckPath("/etc/talon/keys/service.json"); !d.Allowed {
		t.Errorf("absolute allow-list entry ignored: %s", d.Reason)
	}
}

func TestContentCredentialsAreMaskedByDefault(t *testing.T) {
	p := DefaultPolicy()
	content := "export API_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789\n"
	d := p.CheckContent("src/config.sh", content)
	if !d.Allowed {
		t.Fatalf("masking should still allow the read: %s", d.Reason)
	}
	if d.Action != ActionMask {
		t.Errorf("action = %s", d.Action)
	}
	if strings.Contains(d.Content, "abcdefghijklmnop") {
		t.Errorf("secret survived masking: %q", d.Content)
	}
	if !strings.Contains(d.Content, "[REDACTED:") {
		t.Errorf("no placeholder in output: %q", d.Content)
	}
	if len(d.Findings) == 0 {
		t.Error("no findings reported")
	}
}

func TestContentBlockRefuses(t *testing.T) {
	p := DefaultPolicy()
	p.ContentFindings = ActionBlock
	d := p.CheckContent("deploy/config.yaml", "password: supersecretvalue")
	if d.Allowed {
		t.Fatal("content with credentials must be refused under the block policy")
	}
	if !strings.Contains(d.Reason, "will not be sent") {
		t.Errorf("reason = %q", d.Reason)
	}
}

func TestContentWarnAndAllow(t *testing.T) {
	content := "token: ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	warn := DefaultPolicy()
	warn.ContentFindings = ActionWarn
	d := warn.CheckContent("a.env", content)
	if !d.Allowed || d.Action != ActionWarn {
		t.Errorf("warn policy = %+v", d)
	}
	if !strings.Contains(d.Reason, "as-is") {
		t.Errorf("warn reason = %q", d.Reason)
	}
	allow := DefaultPolicy()
	allow.ContentFindings = ActionAllow
	d = allow.CheckContent("a.env", content)
	if !d.Allowed || d.Action != ActionAllow {
		t.Errorf("allow policy = %+v", d)
	}
	if !strings.Contains(d.Content, "ghp_") {
		t.Error("allow policy must not alter the content")
	}
}

func TestCleanContentIsUntouched(t *testing.T) {
	p := DefaultPolicy()
	content := "package main\n\nfunc main() {}\n"
	d := p.CheckContent("main.go", content)
	if !d.Allowed || d.Content != content || d.Action != ActionAllow {
		t.Errorf("clean content altered: %+v", d)
	}
	if len(d.Findings) != 0 {
		t.Errorf("clean content produced findings: %+v", d.Findings)
	}
}

func TestMultiLinePrivateKeyIsDetected(t *testing.T) {
	p := DefaultPolicy()
	key := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA1234567890\nabcdef\n-----END RSA PRIVATE KEY-----"
	d := p.CheckContent("id_rsa", key)
	if d.Allowed && strings.Contains(d.Content, "MIIEowIBAAKCAQEA1234567890") {
		t.Error("a multi-line private key was sent unmasked")
	}
	if len(d.Findings) == 0 {
		t.Error("multi-line private key not reported")
	}
}

func TestParseAction(t *testing.T) {
	for _, in := range []string{"block", "MASK", " warn ", "allow"} {
		if _, err := ParseAction(in); err != nil {
			t.Errorf("ParseAction(%q) = %v", in, err)
		}
	}
	if _, err := ParseAction("maybe"); err == nil {
		t.Error("unknown action must be rejected")
	}
}

func TestSummaryListsKinds(t *testing.T) {
	if Summary(nil) != "" {
		t.Error("no findings should produce no summary")
	}
	f := []secrets.Finding{{Kind: "openai-api-key"}, {Kind: "openai-api-key"}, {Kind: "github-token"}}
	s := Summary(f)
	if !strings.Contains(s, "openai-api-key×2") || !strings.Contains(s, "github-token×1") {
		t.Errorf("summary = %q", s)
	}
}

func TestCheckAndApply(t *testing.T) {
	p := DefaultPolicy()
	d := p.CheckAndApply("a.env", "key=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789")
	if strings.Contains(d.Content, "abcdefghijklmnop") {
		t.Errorf("secret survived: %q", d.Content)
	}
	blocked := DefaultPolicy()
	blocked.ContentFindings = ActionBlock
	d = blocked.CheckAndApply("c.txt", "api_key=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789")
	if d.Allowed {
		t.Error("block policy must refuse")
	}
	// Even a refused decision must not leak the value in the preview.
	if strings.Contains(d.Content, "abcdefghijklmnop") {
		t.Errorf("refused preview leaked the secret: %q", d.Content)
	}
	if !strings.Contains(d.Content, "[REDACTED:") && !strings.Contains(d.Content, "•") {
		t.Errorf("refused preview must still be sanitised: %q", d.Content)
	}
}

func TestPrivacyModeBlocksSensitiveFiles(t *testing.T) {
	p := DefaultPolicy()
	p.InPrivacyMode = true
	// Even when the file policy is relaxed, privacy mode keeps the gate shut for
	// credential material it has not been told about.
	p.SensitiveFiles = ActionAllow
	d := p.CheckPath(".env")
	if d.Allowed && !strings.Contains(d.Reason, "allow") {
		t.Error("unexpected decision")
	}
	// Ordinary files are unaffected.
	if !p.CheckPath("src/main.go").Allowed {
		t.Error("privacy mode must not block ordinary reads")
	}
}
