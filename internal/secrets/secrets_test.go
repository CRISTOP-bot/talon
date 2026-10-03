package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMaskKeepsPrefixAndTail(t *testing.T) {
	cases := map[string]string{
		"sk-abcdefghijklmnopqrstuvwxyz012345": "sk-" + strings.Repeat("•", 28) + "2345",
		"ghp_1234567890abcdefghij":            "ghp_" + strings.Repeat("•", 16) + "ghij",
		"AKIAIOSFODNN7EXAMPLE":                "AKIA" + strings.Repeat("•", 12) + "MPLE",
		"short":                               strings.Repeat("•", 5),
	}
	for in, want := range cases {
		if got := Mask(in); got != want {
			t.Errorf("Mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskNeverRevealsTheMiddle(t *testing.T) {
	secret := "sk-proj-0123456789abcdefghijklmnopqrstuvwxyz"
	masked := Mask(secret)
	if strings.Contains(masked, "0123456789") {
		t.Errorf("mask leaked the body: %q", masked)
	}
	if !strings.HasPrefix(masked, "sk-") {
		t.Errorf("mask lost the prefix: %q", masked)
	}
	if !strings.HasSuffix(masked, "wxyz") {
		t.Errorf("mask lost the tail: %q", masked)
	}
}

func TestDetectProviderKeys(t *testing.T) {
	text := `ANTHROPIC_API_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789
OPENAI=sk-proj-abcdefghijklmnopqrstuvwxyz012345
GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789
AWS=AKIAIOSFODNN7EXAMPLE
GOOGLE=AIzaSyA1234567890abcdefghijklmnopqrstuvw
SLACK=xox-REMOVED-BY-FILTER
JWT=eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U
`
	findings := Detect(text)
	if len(findings) < 7 {
		t.Fatalf("findings = %d: %+v", len(findings), findings)
	}
	kinds := map[string]bool{}
	for _, f := range findings {
		kinds[f.Kind] = true
	}
	for _, want := range []string{
		"anthropic-api-key", "openai-api-key", "github-token",
		"aws-access-key", "google-api-key", "slack-token", "json-web-token",
	} {
		if !kinds[want] {
			t.Errorf("missing %s in %v", want, kinds)
		}
	}
}

func TestDetectPrivateKeysAndPasswords(t *testing.T) {
	key := "-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA\n-----END RSA PRIVATE KEY-----"
	if len(Detect(key)) == 0 {
		t.Error("private key not detected")
	}
	conf := "db:\n  password: hunter2verysecret\n  user: admin\n"
	f := Detect(conf)
	if len(f) == 0 || f[0].Kind != "password-assignment" {
		t.Errorf("password not detected: %+v", f)
	}
	if strings.Contains(conf, "[REDACTED") {
		t.Error("Detect must not mutate the input")
	}
}

func TestDetectConnectionStrings(t *testing.T) {
	text := "DATABASE_URL=postgres://admin:s3cr3tpass@db.internal:5432/app"
	f := Detect(text)
	if len(f) == 0 {
		t.Fatal("connection string credential not detected")
	}
	if !strings.Contains(f[0].Kind, "connection") {
		t.Errorf("kind = %s", f[0].Kind)
	}
}

func TestNoFalsePositivesOnOrdinaryText(t *testing.T) {
	text := strings.Join([]string{
		"# Fix the parser",
		"Run `go test ./...` and update the token stream in parser.go.",
		"The build takes 42 seconds; see docs/architecture.md.",
		"https://example.com/path?query=1&other=2",
		`func handle(w http.ResponseWriter, r *http.Request) { fmt.Fprintln(w, "hi") }`,
		"password = os.Getenv(\"DB_PASSWORD\")",
	}, "\n")
	if f := Detect(text); len(f) != 0 {
		t.Errorf("false positives: %+v", f)
	}
}

func TestPlaceholdersAreIgnored(t *testing.T) {
	text := "api_key: your-api-key-here\ntoken: xxxxxxxxxxxxxxxx\nsecret: CHANGEME"
	if f := Detect(text); len(f) != 0 {
		t.Errorf("placeholders should not be flagged: %+v", f)
	}
}

func TestRedactBulletsStyle(t *testing.T) {
	secret := "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"
	text := "sending with key " + secret + " now"
	cleaned, findings := Redact(text, Bullets)
	if strings.Contains(cleaned, secret) {
		t.Errorf("secret survived redaction: %q", cleaned)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	if !strings.Contains(cleaned, "sk-ant-") {
		t.Errorf("prefix not preserved: %q", cleaned)
	}
	if !strings.Contains(cleaned, "•") {
		t.Errorf("no bullets in output: %q", cleaned)
	}
}

func TestRedactPlaceholderStyle(t *testing.T) {
	secret := "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	cleaned, findings := Redact("token="+secret, Placeholder)
	if strings.Contains(cleaned, secret) {
		t.Errorf("secret survived: %q", cleaned)
	}
	if !strings.Contains(cleaned, "[REDACTED:github-token]") {
		t.Errorf("placeholder missing: %q", cleaned)
	}
	if len(findings) != 1 || findings[0].Kind != "github-token" {
		t.Errorf("findings = %+v", findings)
	}
}

func TestRedactExtraLiterals(t *testing.T) {
	secret := "totally-opaque-value-1234"
	cleaned, findings := Redact("the key is "+secret, Bullets, secret)
	if strings.Contains(cleaned, secret) {
		t.Errorf("literal secret survived: %q", cleaned)
	}
	if len(findings) != 0 {
		t.Errorf("an unrecognised literal should not be a finding: %+v", findings)
	}
}

func TestRedactShortLiteralsAreIgnored(t *testing.T) {
	cleaned, _ := Redact("abc def", Bullets, "abc")
	if cleaned != "abc def" {
		t.Errorf("short value should not be redacted: %q", cleaned)
	}
}

func TestRedactMultipleSecrets(t *testing.T) {
	text := strings.Join([]string{
		"sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789",
		"AKIAIOSFODNN7EXAMPLE",
		"password: correcthorsebatterystaple",
	}, "\n")
	cleaned, findings := Redact(text, Bullets)
	if len(findings) != 3 {
		t.Fatalf("findings = %d: %+v", len(findings), findings)
	}
	for _, f := range findings {
		if strings.Contains(cleaned, text[f.Start:f.End]) {
			t.Errorf("secret of kind %s survived", f.Kind)
		}
	}
}

func TestRedactIsIdempotent(t *testing.T) {
	text := "key sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"
	once, _ := Redact(text, Placeholder)
	twice, findings := Redact(once, Placeholder)
	if len(findings) != 0 {
		t.Errorf("redacted output still matches a rule: %+v", findings)
	}
	if twice != once {
		t.Errorf("not idempotent: %q vs %q", once, twice)
	}
}

func TestDetectFileWithLineNumbers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.env")
	body := "SAFE=1\nAPI_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789\nOTHER=2\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	findings, err := DetectFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 1 {
		t.Fatalf("findings = %+v", findings)
	}
	if findings[0].Line != 2 {
		t.Errorf("line = %d, want 2", findings[0].Line)
	}
	if findings[0].File != path {
		t.Errorf("file = %q", findings[0].File)
	}
}

func TestDetectFileRefusesHugeFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "big.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(9 << 20); err != nil {
		t.Fatal(err)
	}
	if _, err := DetectFile(path); err == nil {
		t.Error("expected an error for a file that is too large to scan")
	}
}

func TestScanLinesNumbersLines(t *testing.T) {
	text := "line one\nline two\nghp_abcdefghijklmnopqrstuvwxyz0123456789\n"
	findings := ScanLines(text)
	if len(findings) != 1 || findings[0].Line != 3 {
		t.Errorf("findings = %+v", findings)
	}
}

func TestIsSensitiveEnv(t *testing.T) {
	sensitive := []string{
		"AI_API_KEY", "ANTHROPIC_API_KEY", "AWS_SECRET_ACCESS_KEY", "GITHUB_TOKEN",
		"MY_DB_PASSWORD", "SOME_TOKEN", "SESSION_SECRET", "COOKIE", "SSH_AUTH_SOCK",
	}
	for _, name := range sensitive {
		if !IsSensitiveEnv(name) {
			t.Errorf("%s should be sensitive", name)
		}
	}
	safe := []string{"PATH", "HOME", "LANG", "GOPATH", "EDITOR", "TALON_THEME"}
	for _, name := range safe {
		if IsSensitiveEnv(name) {
			t.Errorf("%s should not be sensitive", name)
		}
	}
}

func TestFilterEnvDropsCredentials(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"AI_API_KEY=sk-secret-value-here",
		"LANG=en_US.UTF-8",
		"MY_TOKEN=abc",
		"GITHUB_TOKEN=ghp_x",
	}
	filtered := FilterEnv(env, nil, false)
	joined := strings.Join(filtered, ",")
	if strings.Contains(joined, "sk-secret-value-here") {
		t.Errorf("api key leaked through FilterEnv: %s", joined)
	}
	for _, gone := range []string{"AI_API_KEY", "MY_TOKEN", "GITHUB_TOKEN"} {
		if strings.Contains(joined, gone) {
			t.Errorf("%s should have been removed: %s", gone, joined)
		}
	}
	if !strings.Contains(joined, "PATH=/usr/bin") || !strings.Contains(joined, "LANG=") {
		t.Errorf("harmless variables were dropped: %s", joined)
	}
	// Explicitly allowed credentials survive.
	kept := FilterEnv(env, []string{"AI_API_KEY"}, false)
	if !strings.Contains(strings.Join(kept, ","), "AI_API_KEY") {
		t.Errorf("allowlist ignored: %v", kept)
	}
	// Privacy mode for children can keep everything if explicitly requested.
	all := FilterEnv(env, nil, true)
	if len(all) != len(env) {
		t.Errorf("keepSensitive was ignored: %v", all)
	}
}

func TestIsSensitivePath(t *testing.T) {
	sensitive := []string{
		".env", "config/.env.local", "/home/me/.ssh/id_rsa", "certs/server.pem",
		"deploy/kubeconfig", ".npmrc", ".git-credentials", "keys/app.p12",
		"gcloud-service-key.json", "config/credentials.yaml", "home/.aws/credentials",
	}
	for _, p := range sensitive {
		if !IsSensitivePath(p) {
			t.Errorf("%s should be sensitive", p)
		}
	}
	safe := []string{
		"src/main.go", "README.md", "docs/configuration.md", "internal/ui/theme.go",
		"testdata/fixtures/keys.md",
	}
	for _, p := range safe {
		if IsSensitivePath(p) {
			t.Errorf("%s should not be sensitive", p)
		}
	}
}

func TestEnvironmentDumpMasksCredentials(t *testing.T) {
	out := EnvironmentDump([]string{
		"PATH=/usr/bin",
		"AI_API_KEY=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789",
		"AUTH_HEADER=authorization: Bearer abcdefghijklmnop",
	})
	if strings.Contains(out, "abcdefghijklmnop") {
		t.Errorf("environment dump leaked a secret:\n%s", out)
	}
	if !strings.Contains(out, "PATH=/usr/bin") {
		t.Errorf("harmless variable missing:\n%s", out)
	}
	if !strings.Contains(out, "•") {
		t.Errorf("no masking happened:\n%s", out)
	}
}

func TestLooksHighEntropy(t *testing.T) {
	if !LooksHighEntropy("Xy9kQ2mLp7Rt4ZbW8Nc3Vh6Jd1") {
		t.Error("random-looking value not flagged")
	}
	if LooksHighEntropy("hello world this is a sentence") {
		t.Error("prose flagged as high entropy")
	}
	if LooksHighEntropy("short") {
		t.Error("short value flagged")
	}
}

func TestEntropyOrdering(t *testing.T) {
	if Entropy("aaaaaaaa") >= Entropy("aB3xQ9zK") {
		t.Error("entropy should reward variety")
	}
}

func TestFingerprintIsStableAndOpaque(t *testing.T) {
	a := Fingerprint("secret-value-one")
	b := Fingerprint("secret-value-one")
	if a != b {
		t.Error("fingerprint is not stable")
	}
	if strings.Contains(a, "secret") {
		t.Errorf("fingerprint leaks the input: %q", a)
	}
	if len(a) != 8 {
		t.Errorf("fingerprint length = %d", len(a))
	}
}

func TestNonceIsRandom(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		n := Nonce()
		if seen[n] {
			t.Fatalf("duplicate nonce %q", n)
		}
		seen[n] = true
	}
}

func TestFindingStringHidesTheSecret(t *testing.T) {
	f := Finding{Kind: "openai-api-key", Line: 3, File: "/tmp/a.env", Excerpt: Mask("sk-abcdefghijklmnop1234")}
	s := f.String()
	if strings.Contains(s, "abcdef") {
		t.Errorf("finding string leaked: %q", s)
	}
	if !strings.Contains(s, "openai-api-key") || !strings.Contains(s, "/tmp/a.env") {
		t.Errorf("finding string = %q", s)
	}
}

func TestContainsSecret(t *testing.T) {
	if !ContainsSecret("key=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789") {
		t.Error("secret not detected")
	}
	if ContainsSecret("nothing to see here") {
		t.Error("false positive")
	}
}
