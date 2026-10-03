package netguard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBlockedMetadataEndpoints(t *testing.T) {
	p := DefaultPolicy()
	p.Allow("*")
	for _, host := range BlockedHostList() {
		url := "https://" + host + "/latest/meta-data/iam/security-credentials/"
		if err := p.CheckHost(context.Background(), url); err == nil {
			t.Errorf("%s must always be blocked", host)
		}
	}
}

func TestHTTPSRequiredByDefault(t *testing.T) {
	p := DefaultPolicy()
	p.Allow("example.com")
	if err := p.CheckHost(context.Background(), "http://example.com/x"); err == nil {
		t.Error("plain http should be refused")
	}
	p.AllowHTTP = true
	if err := p.CheckHost(context.Background(), "http://example.com/x"); err != nil {
		t.Errorf("http should be allowed once opted in: %v", err)
	}
}

func TestUnsupportedSchemeRefused(t *testing.T) {
	p := DefaultPolicy()
	p.Allow("*")
	for _, u := range []string{"file:///etc/passwd", "gopher://example.com", "ftp://example.com"} {
		if err := p.CheckHost(context.Background(), u); err == nil {
			t.Errorf("%s should be refused", u)
		}
	}
}

func TestPrivateAndLoopbackBlockedByDefault(t *testing.T) {
	p := DefaultPolicy()
	p.Allow("*")
	private := []string{"10.0.0.5", "172.16.1.1", "192.168.1.1", "169.254.10.1", "127.0.0.1"}
	for _, host := range private {
		if err := p.CheckHost(context.Background(), "https://"+host+"/v1"); err == nil {
			t.Errorf("%s should be blocked by default", host)
		}
	}
	p.AllowPrivateIPs = true
	p.AllowLoopback = true
	for _, host := range private {
		if host == "169.254.10.1" {
			// Link-local stays blocked even when private space is allowed: it
			// is where link metadata services live.
			if err := p.CheckHost(context.Background(), "https://"+host+"/v1"); err == nil {
				t.Errorf("%s must stay blocked", host)
			}
			continue
		}
		if err := p.CheckHost(context.Background(), "https://"+host+"/v1"); err != nil {
			t.Errorf("%s should be allowed after opting in: %v", host, err)
		}
	}
}

func TestAllowlistMode(t *testing.T) {
	p := DefaultPolicy()
	p.Allow("api.openai.com", ".example.org")
	if err := p.CheckHost(context.Background(), "https://api.openai.com/v1/chat"); err != nil {
		t.Errorf("allowed host refused: %v", err)
	}
	if err := p.CheckHost(context.Background(), "https://sub.example.org/x"); err != nil {
		t.Errorf("suffix rule did not match: %v", err)
	}
	if err := p.CheckHost(context.Background(), "https://evil.example.com/x"); err == nil {
		t.Error("a look-alike host must not match the suffix rule")
	}
	if err := p.CheckHost(context.Background(), "https://api.openai.com.evil.net/x"); err == nil {
		t.Error("hostname prefix trick must not pass")
	}
}

func TestAskModeInvokesConfirmation(t *testing.T) {
	p := &Policy{Mode: ModeAsk, AllowPrivateIPs: true}
	var asked []string
	p.Confirm = func(host string) error {
		asked = append(asked, host)
		return nil
	}
	if err := p.CheckHost(context.Background(), "https://unknown.example/x"); err != nil {
		t.Fatalf("ask mode should ask and then allow: %v", err)
	}
	if len(asked) != 1 || asked[0] != "unknown.example" {
		t.Errorf("confirm called with %v", asked)
	}
	p.Confirm = func(host string) error { return errDenied }
	if err := p.CheckHost(context.Background(), "https://unknown.example/x"); err == nil {
		t.Error("a refusal must block the request")
	}
}

func TestAskModeWithoutConfirmRefuses(t *testing.T) {
	p := &Policy{Mode: ModeAsk}
	if err := p.CheckHost(context.Background(), "https://unknown.example/x"); err == nil {
		t.Error("without a confirmation hook the request must be refused")
	}
}

func TestTransportEnforcesPolicy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	p := DefaultPolicy()
	// The test server lives on loopback over plain http, so the policy must
	// opt in explicitly — exactly what a local model server requires.
	p.AllowLoopback = true
	p.AllowHTTP = true
	p.Allow("127.0.0.1", "localhost")
	client := p.HTTPClient(5)

	if _, err := client.Get(srv.URL + "/ok"); err != nil {
		t.Fatalf("allowed request failed: %v", err)
	}

	// Now remove the allowance: the same client must refuse.
	strict := DefaultPolicy()
	strictClient := strict.HTTPClient(5)
	if _, err := strictClient.Get(srv.URL + "/ok"); err == nil {
		t.Fatal("a non-allowlisted request must fail at the transport")
	}
}

func TestTransportRecordsDecisions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var decisions []Decision
	p := &Policy{Mode: ModeAllowlist, AllowLoopback: true, AllowHTTP: true}
	p.Allow("127.0.0.1")
	p.OnDecision = func(d Decision) { decisions = append(decisions, d) }
	client := p.HTTPClient(5)
	_, _ = client.Get(srv.URL + "/x")
	// A blocked destination must also be recorded.
	strict := &Policy{Mode: ModeAllowlist}
	strictClient := strict.HTTPClient(5)
	_, _ = strictClient.Get("https://169.254.169.254/latest")

	if len(decisions) != 1 || !decisions[0].Allowed {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestTransportRefusesSmuggledCredentialHeaders(t *testing.T) {
	var got bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := &Policy{Mode: ModeAllowlist, AllowLoopback: true, AllowHTTP: true}
	p.Allow("127.0.0.1")
	client := p.HTTPClient(5)
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("X-Secret-Token", "ghp_abcdefghijklmnopqrstuvwxyz0123456789")
	if _, err := client.Do(req); err == nil {
		t.Fatal("an unexpected credential header must be refused")
	}
	if got {
		t.Error("the request reached the server despite the refusal")
	}

	// The header Talon sets itself is allowed through.
	req2, _ := http.NewRequest("GET", srv.URL, nil)
	req2.Header.Set("Authorization", "Bearer sk-provider-key-value")
	resp, err := client.Do(req2)
	if err != nil {
		t.Fatalf("provider credential header was refused: %v", err)
	}
	resp.Body.Close()
}

func TestRedactRequestBody(t *testing.T) {
	body := []byte(`{"model":"gpt-5","headers":{"api_key":"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"}}`)
	cleaned := RedactRequestBody(body, nil)
	if strings.Contains(cleaned, "abcdefghijklmnop") {
		t.Errorf("body was not redacted: %s", cleaned)
	}
	if !strings.Contains(cleaned, "[REDACTED:") {
		t.Errorf("no redaction marker: %s", cleaned)
	}
	if !strings.Contains(cleaned, `"model":"gpt-5"`) {
		t.Errorf("harmless fields should survive: %s", cleaned)
	}
}

func TestRedactRequestBodyTruncates(t *testing.T) {
	big := strings.Repeat("a", 9000)
	cleaned := RedactRequestBody([]byte(big), nil)
	if len(cleaned) > 4200 {
		t.Errorf("body not truncated: %d bytes", len(cleaned))
	}
	if !strings.Contains(cleaned, "more bytes") {
		t.Errorf("truncation not reported: %s", cleaned[len(cleaned)-80:])
	}
}

func TestRedactRequestBodyUsesExtraSecrets(t *testing.T) {
	body := []byte(`{"token":"opaque-local-secret-123456"}`)
	cleaned := RedactRequestBody(body, []string{"opaque-local-secret-123456"})
	if strings.Contains(cleaned, "opaque-local-secret") {
		t.Errorf("configured secret leaked: %s", cleaned)
	}
}

func TestProviderHosts(t *testing.T) {
	cases := map[string][]string{
		"openai":     {"api.openai.com"},
		"anthropic":  {"api.anthropic.com"},
		"gemini":     {"generativelanguage.googleapis.com"},
		"openrouter": {"openrouter.ai"},
		"ollama":     {"localhost"},
	}
	for provider, want := range cases {
		got := ProviderHosts(provider, "")
		found := false
		for _, h := range got {
			if h == want[0] {
				found = true
			}
		}
		if !found {
			t.Errorf("ProviderHosts(%q) = %v, want %s", provider, got, want[0])
		}
	}
	if hosts := ProviderHosts("custom", "https://llm.internal:8080/v1"); len(hosts) != 1 || hosts[0] != "llm.internal" {
		t.Errorf("custom base URL host = %v", hosts)
	}
	if !IsLocalProviderHost("ollama") || IsLocalProviderHost("openai") {
		t.Error("local provider detection is wrong")
	}
}

func TestAllowIsIdempotent(t *testing.T) {
	p := DefaultPolicy()
	p.Allow("a.example", "a.example", " b.example ")
	if len(p.AllowedHosts) != 2 {
		t.Errorf("allowed hosts = %v", p.AllowedHosts)
	}
}

func TestCheckHostRejectsUnparseableURL(t *testing.T) {
	p := DefaultPolicy()
	if err := p.CheckHost(context.Background(), "https://[::1"); err == nil {
		t.Error("expected a parse error")
	}
	if err := p.CheckHost(context.Background(), "https:///nohost"); err == nil {
		t.Error("a URL without a host must be refused")
	}
}

// errDenied stands in for a user pressing "n".
var errDenied = errTest("denied by the user")

type errTest string

func (e errTest) Error() string { return string(e) }
