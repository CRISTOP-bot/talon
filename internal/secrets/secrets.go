// Package secrets detects and redacts credentials in text that Talon is about
// to write to a log, print to the terminal, store in memory, or transmit to a
// model provider.
//
// It is a defensive component, not a guarantee: pattern matching cannot find
// every credential, which is why Talon also minimises what it reads and sends.
// But it turns the common cases into hard failures instead of silent leaks.
package secrets

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Finding describes one detected secret.
type Finding struct {
	// Kind is a stable identifier such as "openai-api-key".
	Kind string `json:"kind"`
	// Line is the 1-based line number, or 0 when unknown.
	Line int `json:"line,omitempty"`
	// File is the file the finding came from, when applicable.
	File string `json:"file,omitempty"`
	// Start and End delimit the secret inside the scanned text.
	Start int `json:"start"`
	End   int `json:"end"`
	// Excerpt is a masked preview safe to display.
	Excerpt string `json:"excerpt"`
	// rule is the pattern that matched, for tests and explanations.
	rule string
}

// String renders the finding without the secret.
func (f Finding) String() string {
	if f.File != "" {
		return fmt.Sprintf("%s (%s line %d)", f.Kind, f.File, f.Line)
	}
	if f.Line > 0 {
		return fmt.Sprintf("%s (line %d)", f.Kind, f.Line)
	}
	return f.Kind
}

// rule is one detection pattern.
type rule struct {
	name string
	// pattern must capture the secret in group 1.
	pattern *regexp.Regexp
	// prefixGroup, when > 0, names a capture group holding a visible prefix
	// (such as "sk-") that should survive masking.
	prefixGroup int
	// fixedFormat marks rules that match a provider-specific credential shape.
	// Their values are never filtered through the placeholder heuristic: a real
	// key can legitimately contain the word "example".
	fixedFormat bool
}

// Rules are the patterns Talon detects. They are deliberately specific: a rule
// that fires on ordinary prose would train users to ignore the warnings.
var rules = []rule{
	{
		name:        "private-key",
		pattern:     regexp.MustCompile(`(?m)(-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----[\s\S]*?-----END (?:[A-Z ]+ )?PRIVATE KEY-----)`),
		fixedFormat: true,
	},
	{
		name:        "openssh-private-key",
		pattern:     regexp.MustCompile(`(?m)(-----BEGIN OPENSSH PRIVATE KEY-----[\s\S]*?-----END OPENSSH PRIVATE KEY-----)`),
		fixedFormat: true,
	},
	{
		name:        "anthropic-api-key",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b(sk-ant-[A-Za-z0-9_\-]{24,})`),
	},
	{
		name:        "openai-api-key",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b(sk-(?:proj-)?[A-Za-z0-9_\-]{20,})`),
	},
	{
		name:        "openai-legacy-key",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b(sk-[A-Za-z0-9]{32})`),
	},
	{
		name:        "github-token",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b((?:ghp|gho|ghu|ghs|ghr|github_pat)_[A-Za-z0-9_]{20,})`),
	},
	{
		name:        "gitlab-token",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b(glpat-[A-Za-z0-9_\-]{20,})`),
	},
	{
		name:        "slack-token",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b(xox[abprs]-[A-Za-z0-9-]{10,})`),
	},
	{
		name:        "aws-access-key",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b((?:AKIA|ASIA|ABIA|ACCA)[0-9A-Z]{16})`),
	},
	{
		name:        "google-api-key",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b(AIza[0-9A-Za-z_\-]{35})`),
	},
	{
		name:        "stripe-secret-key",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b((?:sk|rk)_(?:live|test)_[A-Za-z0-9]{16,})`),
	},
	{
		name:        "npm-token",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b(npm_[A-Za-z0-9]{30,})`),
	},
	{
		name:        "huggingface-token",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b(hf_[A-Za-z0-9]{30,})`),
	},
	{
		name:        "sendgrid-key",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b(SG\.[A-Za-z0-9_\-]{16,}\.[A-Za-z0-9_\-]{16,})`),
	},
	{
		name:        "json-web-token",
		prefixGroup: 1,
		fixedFormat: true,
		pattern:     regexp.MustCompile(`\b(eyJ[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,}\.[A-Za-z0-9_\-]{8,})`),
	},
	{
		name:    "authorization-header",
		pattern: regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*(?:bearer|basic|token)\s+[A-Za-z0-9._\-+/=]{12,})`),
	},
	{
		name:    "cookie-header",
		pattern: regexp.MustCompile(`(?i)\b((?:set-)?cookie\s*[:=]\s*[^\s;"']{16,})`),
	},
	{
		name:    "password-assignment",
		pattern: regexp.MustCompile(`(?i)\b((?:password|passwd|pwd|passphrase)\s*[:=]\s*["']?[^\s"',;}]{6,})`),
	},
	{
		name:    "secret-assignment",
		pattern: regexp.MustCompile(`(?i)\b((?:api[_-]?key|apikey|access[_-]?key|secret[_-]?key|client[_-]?secret|auth[_-]?token|access[_-]?token|refresh[_-]?token|bearer[_-]?token|token|secret|credential[s]?)\s*[:=]\s*["']?[^\s"',;}]{8,})`),
	},
	{
		name:    "connection-string-credential",
		pattern: regexp.MustCompile(`(?i)\b((?:postgres|postgresql|mysql|mongodb(?:\+srv)?|redis|amqp)://[^\s:@/]+:[^\s@/]{4,}@[^\s]+)`),
	},
	{
		name:    "basic-auth-url",
		pattern: regexp.MustCompile(`\b([a-z][a-z0-9+.\-]*://[^\s:/@]+:[^\s@/]{4,}@[^\s]+)`),
	},
}

// Detect returns every secret found in text, sorted by position.
func Detect(text string) []Finding {
	return detect(text, "")
}

// DetectFile scans a file and returns findings with file and line information.
func DetectFile(path string) ([]Finding, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.Size() > 8<<20 {
		// Refuse to read very large files for scanning; callers should treat
		// this as "not scanned" rather than "clean".
		return nil, fmt.Errorf("file is too large to scan (%d bytes)", info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	findings := detect(string(data), path)
	// Attach line numbers.
	for i := range findings {
		findings[i].Line = lineOf(string(data), findings[i].Start)
	}
	return findings, nil
}

func lineOf(text string, offset int) int {
	if offset > len(text) {
		return 0
	}
	return 1 + strings.Count(text[:offset], "\n")
}

//nolint:gocyclo // a detector is inherently a loop over patterns
func detect(text string, file string) []Finding {
	if text == "" {
		return nil
	}
	var findings []Finding
	claimed := make([]bool, len(text))
	for _, r := range rules {
		for _, m := range r.pattern.FindAllStringSubmatchIndex(text, -1) {
			start, end := m[0], m[1]
			if r.prefixGroup > 0 {
				if 2*r.prefixGroup+1 >= len(m) || m[2*r.prefixGroup] < 0 {
					continue
				}
				start, end = m[2*r.prefixGroup], m[2*r.prefixGroup+1]
			}
			// Skip anything already covered by a more specific rule.
			overlaps := false
			for i := start; i < end; i++ {
				if claimed[i] {
					overlaps = true
					break
				}
			}
			if overlaps {
				continue
			}
			for i := start; i < end && i < len(claimed); i++ {
				claimed[i] = true
			}
			secret := text[start:end]
			if strings.Count(secret, "*") > 2 {
				continue
			}
			if !r.fixedFormat && (looksLikePlaceholder(secret) || looksLikeCodeReference(secret)) {
				continue
			}
			findings = append(findings, Finding{
				Kind:    r.name,
				File:    file,
				Start:   start,
				End:     end,
				Excerpt: Mask(secret),
				rule:    r.name,
			})
		}
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].Start < findings[j].Start })
	return findings
}

// looksLikeCodeReference filters values that are code rather than credentials,
// such as `os.Getenv("DB_PASSWORD")` or `${TOKEN}`.
func looksLikeCodeReference(v string) bool {
	if strings.Contains(v, "Getenv(") || strings.Contains(v, "(") ||
		strings.Contains(v, "$") || strings.Contains(v, "{{") {
		return true
	}
	return false
}

// looksLikePlaceholder filters values such as "your-api-key-here".
func looksLikePlaceholder(v string) bool {
	lower := strings.ToLower(v)
	for _, p := range []string{
		"your", "example", "placeholder", "changeme", "change_me", "xxxx",
		"todo", "replace", "dummy", "redacted", "<", "${", "insert",
	} {
		if strings.Contains(lower, p) {
			return true
		}
	}
	return false
}

// Mask renders a secret so it can be shown to a human: the recognizable prefix
// and the last four characters survive, the middle becomes bullets.
//
//	sk-abcdefghijklmnop1234  →  sk-••••••••••••••••1234
func Mask(secret string) string {
	if secret == "" {
		return ""
	}
	runes := []rune(secret)
	if len(runes) <= 8 {
		return strings.Repeat("•", len(runes))
	}
	prefix := ""
	body := secret
	// Keep a provider-specific prefix such as "sk-", "ghp_", "xoxb-", "AIza".
	switch {
	case hasUpperPrefix(secret):
		// AWS-style keys have no separator: AKIA…, ASIA…, AROA….
		prefix, body = secret[:4], secret[4:]
	default:
		if idx := strings.IndexAny(secret, "-_"); idx > 0 && idx <= 6 {
			prefix, body = secret[:idx+1], secret[idx+1:]
			// sk-ant-… and sk-proj-… are more recognisable with both segments.
			if prefix == "sk-" {
				if next := strings.IndexAny(body, "-_"); next > 0 && next <= 5 {
					prefix, body = secret[:idx+1+next+1], secret[idx+next+2:]
				}
			}
		} else if idx := strings.Index(secret, "."); idx > 0 && idx <= 4 {
			prefix, body = secret[:idx+1], secret[idx+1:]
		}
	}
	bodyRunes := []rune(body)
	if len(bodyRunes) <= 4 {
		return prefix + strings.Repeat("•", len(bodyRunes))
	}
	tail := string(bodyRunes[len(bodyRunes)-4:])
	middle := len(bodyRunes) - 4
	// Cap the bullet run so a very long token does not produce a huge line.
	const maxBullets = 32
	if middle > maxBullets {
		tail = string(bodyRunes[len(bodyRunes)-8:])
		middle = maxBullets
	}
	return prefix + strings.Repeat("•", middle) + tail
}

// Style selects how a redaction is rendered.
type Style int

// Redaction styles.
const (
	// Bullets masks the secret (sk-••••1234). Used in logs and terminals.
	Bullets Style = iota
	// Placeholder replaces the secret with a stable token that keeps the text
	// readable for the model, e.g. [REDACTED:openai-api-key].
	Placeholder
)

// Redact removes secrets from text, returning the cleaned text and the findings.
// extra values are literal strings to redact even when no pattern matches
// (the configured API key, for instance).
func Redact(text string, style Style, extra ...string) (string, []Finding) {
	if text == "" {
		return text, nil
	}
	cleaned := text
	for _, secret := range extra {
		if len(secret) < 8 {
			continue
		}
		cleaned = strings.ReplaceAll(cleaned, secret, replacement(secret, style))
	}
	findings := Detect(cleaned)
	// Apply from the end so offsets stay valid.
	for i := len(findings) - 1; i >= 0; i-- {
		f := findings[i]
		if f.Start < 0 || f.End > len(cleaned) || f.Start >= f.End {
			continue
		}
		cleaned = cleaned[:f.Start] + replacement(cleaned[f.Start:f.End], style, f.Kind) + cleaned[f.End:]
	}
	return cleaned, findings
}

func replacement(secret string, style Style, kinds ...string) string {
	if style == Placeholder {
		kind := "secret"
		if len(kinds) > 0 {
			kind = kinds[0]
		}
		return "[REDACTED:" + kind + "]"
	}
	return Mask(secret)
}

// ContainsSecret reports whether text holds anything that looks like a secret.
func ContainsSecret(text string) bool { return len(Detect(text)) > 0 }

// hasUpperPrefix reports whether a secret starts with a four-letter uppercase
// provider prefix such as AKIA or ASIA.
func hasUpperPrefix(s string) bool {
	if len(s) < 8 {
		return false
	}
	for i := 0; i < 4; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}

// Fingerprint returns a short, non-reversible identifier for a secret, so logs
// can correlate occurrences without storing the value.
func Fingerprint(secret string) string {
	sum := sha256Sum(secret)
	return hex.EncodeToString(sum[:4])
}

// Nonce returns a random hex string, used for identifiers that must not be
// guessable. It is exported here so callers do not each import crypto/rand.
func Nonce() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%016x", 0)
	}
	return hex.EncodeToString(b)
}

// sensitiveEnvNames are environment variables that must never be forwarded to
// child processes or printed.
var sensitiveEnvNames = []string{
	"AI_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "GEMINI_API_KEY",
	"API_KEY", "TOKEN", "SECRET", "PASSWORD", "PASSWD", "AWS_SECRET_ACCESS_KEY",
	"AWS_ACCESS_KEY_ID", "AWS_SESSION_TOKEN", "GITHUB_TOKEN", "GH_TOKEN",
	"NPM_TOKEN", "HF_TOKEN", "OPENAI_ORG_ID", "DB_PASSWORD", "DATABASE_URL",
	"DATADOG_API_KEY", "SENTRY_DSN", "STRIPE_SECRET_KEY", "SLACK_TOKEN",
	"SSH_AUTH_SOCK", "GPG_TTY", "COOKIE", "SESSION_SECRET", "PRIVATE_KEY",
	"NPM_CONFIG_USERCONFIG", "GOOGLE_APPLICATION_CREDENTIALS",
}

// IsSensitiveEnv reports whether an environment variable must be treated as a
// credential. Matching is suffix-based so GEMINI_API_KEY and MY_API_KEY both
// count.
func IsSensitiveEnv(name string) bool {
	upper := strings.ToUpper(name)
	for _, n := range sensitiveEnvNames {
		if upper == n {
			return true
		}
	}
	for _, suffix := range []string{
		"_API_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_PASSWD", "_CREDENTIALS",
		"_SECRET_KEY", "_ACCESS_KEY", "_PRIVATE_KEY", "_DSN", "_SESSION_SECRET",
	} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}

// FilterEnv returns a copy of env with credential variables removed, keeping
// only the entries the caller explicitly allowed plus non-sensitive ones.
func FilterEnv(env []string, allow []string, keepSensitive bool) []string {
	allowSet := map[string]bool{}
	for _, a := range allow {
		allowSet[strings.ToUpper(a)] = true
	}
	var out []string
	for _, kv := range env {
		name, _, found := strings.Cut(kv, "=")
		if !found {
			continue
		}
		upper := strings.ToUpper(name)
		if IsSensitiveEnv(name) && !keepSensitive && !allowSet[upper] {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// EnvValue reads one variable from a slice of KEY=VALUE entries.
func EnvValue(env []string, name string) (string, bool) {
	for _, kv := range env {
		if k, v, found := strings.Cut(kv, "="); found && k == name {
			return v, true
		}
	}
	return "", false
}

// highEntropyThreshold is the Shannon entropy (bits per character) above which
// an assignment value is treated as a probable credential.
const highEntropyThreshold = 3.4

// Entropy returns the Shannon entropy of s in bits per character.
func Entropy(s string) float64 {
	if s == "" {
		return 0
	}
	counts := map[rune]float64{}
	var total float64
	for _, r := range s {
		counts[r]++
		total++
	}
	var entropy float64
	for _, c := range counts {
		p := c / total
		entropy -= p * math.Log2(p)
	}
	return entropy
}

// LooksHighEntropy reports whether a value looks like a random credential,
// which catches keys whose format Talon does not recognise.
func LooksHighEntropy(value string) bool {
	if len(value) < 20 {
		return false
	}
	classes := 0
	if strings.ContainsAny(value, "abcdef") {
		classes++
	}
	if strings.ContainsAny(value, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
		classes++
	}
	if strings.ContainsAny(value, "0123456789") {
		classes++
	}
	if strings.ContainsAny(value, "-_./+=@") {
		classes++
	}
	if classes < 3 {
		return false
	}
	return Entropy(value) >= highEntropyThreshold
}

// ScanLines scans a text line by line, which keeps memory bounded for large
// files and reports the correct line numbers.
func ScanLines(text string) []Finding {
	var findings []Finding
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	line := 0
	for sc.Scan() {
		line++
		for _, f := range Detect(sc.Text()) {
			f.Line = line
			findings = append(findings, f)
		}
	}
	return findings
}

// SensitiveFilePatterns are path fragments that mark files as credential
// material regardless of their content.
var SensitiveFilePatterns = []string{
	".env", ".env.local", ".env.production", ".envrc",
	"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", ".ssh/", "authorized_keys",
	".pem", ".key", ".p12", ".pfx", ".jks", ".keystore",
	".npmrc", ".pypirc", ".netrc", "_netrc", ".docker/config.json",
	".git-credentials", ".gitconfig.local", "credentials.json", "credentials.yaml",
	".htpasswd", "shadow", "passwd", "secrets.yaml", "secrets.yml", "secrets.json",
	"kubeconfig", ".kube/config", "known_hosts", "auth.json", "token.json",
	".boto", "service-account.json", "gcloud-service-key.json",
	"credentials", ".aws/credentials", ".aws/config", "azureprofile.json",
}

// IsSensitivePath reports whether a path is credential material by name.
func IsSensitivePath(path string) bool {
	normalized := filepath.ToSlash(strings.ToLower(path))
	base := filepath.Base(normalized)
	for _, p := range SensitiveFilePatterns {
		if strings.HasSuffix(p, "/") {
			if strings.Contains(normalized, "/"+p) || strings.HasPrefix(normalized, p) {
				return true
			}
			continue
		}
		if base == p || strings.HasSuffix(normalized, "/"+p) || strings.Contains(base, p) {
			return true
		}
	}
	// Anything that looks like a certificate or a key by extension.
	switch filepath.Ext(base) {
	case ".pfx", ".p12", ".jks", ".keystore", ".crt", ".cer":
		return true
	}
	return false
}

// EnvironmentDump renders the environment in a form that is safe to log.
func EnvironmentDump(env []string) string {
	var b strings.Builder
	for _, kv := range env {
		name, value, found := strings.Cut(kv, "=")
		if !found {
			continue
		}
		if IsSensitiveEnv(name) {
			fmt.Fprintf(&b, "%s=%s\n", name, Mask(value))
			continue
		}
		cleaned, _ := Redact(value, Bullets)
		fmt.Fprintf(&b, "%s=%s\n", name, cleaned)
	}
	return b.String()
}

func sha256Sum(s string) [32]byte {
	return sha256.Sum256([]byte(s))
}
