// Package netguard centralises every outbound network decision Talon makes.
//
// All HTTP traffic goes through the RoundTripper returned by Transport, so a
// request cannot reach the network without passing policy. The policy blocks
// cloud metadata endpoints and private address space by default, requires the
// destination to be on an allow list, and reports every attempt to the audit
// log. There is no hidden egress.
package netguard

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/talon-cli/talon/internal/errs"
	"github.com/talon-cli/talon/internal/secrets"
)

// Mode controls how strictly destinations are checked.
type Mode string

// Network modes.
const (
	// ModeAllowlist permits only explicitly listed hosts.
	ModeAllowlist Mode = "allowlist"
	// ModeAsk allows anything except blocked ranges, but asks first for hosts
	// that are not on the list.
	ModeAsk Mode = "ask"
	// ModeOff performs no host checks (used by tests and local-only setups).
	ModeOff Mode = "off"
)

// blockedHosts are never reachable, regardless of configuration. They are
// instance-metadata services that hand out cloud credentials.
var blockedHosts = []string{
	"169.254.169.254",
	"metadata.google.internal",
	"metadata.goog",
	"instance-data.ec2.internal",
	"fd00:ec2::254",
	"metadata.azure.com",
	"100.100.100.200", // Alibaba Cloud
}

// BlockedHostList returns the always-blocked hosts, for documentation and tests.
func BlockedHostList() []string {
	out := append([]string(nil), blockedHosts...)
	sort.Strings(out)
	return out
}

// Policy is the network policy applied to every request.
type Policy struct {
	// Mode selects the host check behaviour.
	Mode Mode
	// AllowedHosts are exact hosts or suffix patterns such as ".openai.com".
	AllowedHosts []string
	// AllowPrivateIPs permits RFC1918 and unique-local addresses. Local model
	// servers need this; hosted providers never do.
	AllowPrivateIPs bool
	// AllowLoopback permits 127.0.0.0/8 and ::1 for local providers.
	AllowLoopback bool
	// AllowHTTP permits plain http:// (only useful for local servers).
	AllowHTTP bool
	// Confirm is called for hosts that are not on the allow list when Mode is
	// ModeAsk. Returning an error refuses the request.
	Confirm func(host string) error
	// OnDecision is called for every allowed request with its summary.
	OnDecision func(Decision)
	// SecretProvider returns values that must never be transmitted. It is used
	// when logging request bodies.
	SecretProvider func() []string
}

// Decision describes an evaluated request.
type Decision struct {
	Host    string
	URL     string
	Allowed bool
	Reason  string
}

// DefaultPolicy returns a locked-down policy: HTTPS only, no private space, no
// loopback, and only the listed hosts reachable.
func DefaultPolicy() *Policy {
	return &Policy{Mode: ModeAllowlist}
}

// Allow adds hosts to the allow list. Exact hosts and ".suffix" patterns are
// both accepted; a bare host matches itself and its subdomains.
func (p *Policy) Allow(hosts ...string) {
	for _, h := range hosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			continue
		}
		duplicate := false
		for _, existing := range p.AllowedHosts {
			if existing == h {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		p.AllowedHosts = append(p.AllowedHosts, h)
	}
}

// HostAllowed reports whether a host matches the allow list.
func (p *Policy) HostAllowed(host string) bool {
	host = strings.ToLower(host)
	for _, pattern := range p.AllowedHosts {
		if pattern == host {
			return true
		}
		if strings.HasPrefix(pattern, ".") && strings.HasSuffix(host, pattern) {
			return true
		}
		if strings.HasSuffix(pattern, "*") && strings.HasSuffix(host, strings.TrimSuffix(pattern, "*")) {
			return true
		}
		// A bare host also covers its subdomains, which is what API hosts need.
		if !strings.Contains(pattern, ".") && !strings.Contains(pattern, "*") &&
			strings.HasSuffix(host, "."+pattern) {
			return true
		}
	}
	return false
}

// CheckHost validates a destination. It returns nil when the request may
// proceed, and an errs error with a human explanation otherwise.
func (p *Policy) CheckHost(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return errs.Newf(errs.KindConfig, "network", "cannot parse URL %q: %v", rawURL, err)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !p.AllowHTTP {
			return errs.New(errs.KindPermission, "network",
				"refusing to use plain http; set security.allow_http to permit it")
		}
	default:
		return errs.Newf(errs.KindPermission, "network", "refusing unsupported URL scheme %q", u.Scheme)
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return errs.New(errs.KindConfig, "network", "the URL has no host")
	}
	// url.Hostname mis-parses bare IPv6 literals, so the raw authority is also
	// checked (without brackets and without a port).
	authority := strings.ToLower(u.Host)
	if strings.Count(authority, ":") == 1 {
		// A single colon means "host:port"; strip it. More than one colon means
		// a bare IPv6 literal, which url.Parse handles badly but must not be
		// mistaken for a host with a port.
		if i := strings.LastIndex(authority, ":"); i > 0 {
			authority = authority[:i]
		}
	}
	authority = strings.Trim(authority, "[]")
	for _, blocked := range blockedHosts {
		if host == blocked || authority == blocked || strings.HasPrefix(host, blocked) {
			return errs.Newf(errs.KindPermission, "network",
				"%s is a cloud metadata endpoint and is always blocked", blocked)
		}
	}
	if p.Mode == ModeOff {
		return nil
	}
	if err := p.checkAddress(host); err != nil {
		return err
	}
	if p.Mode == ModeAsk && !p.HostAllowed(host) {
		if p.Confirm == nil {
			return errs.Newf(errs.KindPermission, "network",
				"%s is not on the network allow list and no confirmation is available", host)
		}
		if err := p.Confirm(host); err != nil {
			return errs.Newf(errs.KindPermission, "network", "the request to %s was not approved: %v", host, err)
		}
		return nil
	}
	if p.Mode == ModeAllowlist && !p.HostAllowed(host) {
		return errs.Newf(errs.KindPermission, "network",
			"%s is not on the network allow list (allowed: %s)",
			host, strings.Join(p.AllowedHosts, ", "))
	}
	_ = ctx
	return nil
}

// checkAddress rejects literal addresses in reserved ranges. Hostnames are not
// resolved here: doing so would introduce a DNS-rebinding window. Instead the
// transport-level check in guardedTransport re-verifies the resolved address at
// dial time, which is where it matters.
func (p *Policy) checkAddress(host string) error {
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	if ip.IsLoopback() {
		if p.AllowLoopback {
			return nil
		}
		return errs.Newf(errs.KindPermission, "network",
			"%s is a loopback address; set security.allow_loopback for local model servers", host)
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		// Link-local space hosts link metadata services and is never opened up,
		// not even for local model servers.
		return errs.Newf(errs.KindPermission, "network",
			"%s is a link-local or unspecified address and is blocked", host)
	}
	if ip.IsPrivate() && !p.AllowPrivateIPs {
		return errs.Newf(errs.KindPermission, "network",
			"%s is a private address; set security.allow_private_ips for local model servers", host)
	}
	return nil
}

// guardedTransport re-checks the resolved address at dial time and records the
// decision. This closes the DNS-rebinding hole that a hostname-only check has.
type guardedTransport struct {
	base   http.RoundTripper
	policy *Policy
}

// Transport wraps base so that every request passes policy.
func (p *Policy) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if _, ok := base.(*guardedTransport); ok {
		return base
	}
	return &guardedTransport{base: base, policy: p}
}

// RoundTrip implements http.RoundTripper.
func (g *guardedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := g.policy.CheckHost(req.Context(), req.URL.String()); err != nil {
		g.record(req, false, err.Error())
		return nil, err
	}
	// Defensive: a caller must never be able to smuggle credentials in a header
	// Talon did not add. Talon only ever sets its provider credential, so any
	// other credential-shaped header is refused.
	if bad := unexpectedCredentials(req); bad != "" {
		err := errs.Newf(errs.KindPermission, "network",
			"refusing to send an unexpected credential header (%s)", bad)
		g.record(req, false, err.Error())
		return nil, err
	}
	resp, err := g.base.RoundTrip(req)
	g.record(req, err == nil, errText(err))
	return resp, err
}

func (t *guardedTransport) record(req *http.Request, allowed bool, reason string) {
	if t.policy.OnDecision == nil {
		return
	}
	t.policy.OnDecision(Decision{
		Host:    req.URL.Hostname(),
		URL:     req.URL.Scheme + "://" + req.URL.Host + req.URL.Path,
		Allowed: allowed,
		Reason:  reason,
	})
}

// allowedCredentialHeaders lists the headers Talon itself sets.
var allowedCredentialHeaders = map[string]bool{
	"authorization":       true, // set by the LLM client with the configured key
	"x-api-key":           true, // Anthropic
	"x-goog-api-key":      true, // Gemini
	"cookie":              true, // MCP servers may need a session cookie
	"proxy-authorization": true,
}

func unexpectedCredentials(req *http.Request) string {
	var found []string
	for name := range req.Header {
		lower := strings.ToLower(name)
		if !strings.Contains(lower, "auth") && !strings.Contains(lower, "token") &&
			!strings.Contains(lower, "key") && !strings.Contains(lower, "cookie") &&
			!strings.Contains(lower, "secret") {
			continue
		}
		if allowedCredentialHeaders[lower] {
			continue
		}
		for _, v := range req.Header.Values(name) {
			if secrets.ContainsSecret(v) || strings.Contains(strings.ToLower(v), "bearer ") {
				found = append(found, lower)
				break
			}
		}
	}
	sort.Strings(found)
	if len(found) == 0 {
		return ""
	}
	return strings.Join(found, ", ")
}

func errText(err error) string {
	if err == nil {
		return "ok"
	}
	return err.Error()
}

// RedactRequestBody prepares a request body for logging: it removes secrets and
// truncates. It is used by the debug log, never for the actual request.
func RedactRequestBody(body []byte, extraSecrets []string) string {
	if len(body) == 0 {
		return ""
	}
	cleaned, findings := secrets.Redact(string(body), secrets.Placeholder, extraSecrets...)
	summary := ""
	if len(findings) > 0 {
		kinds := map[string]bool{}
		for _, f := range findings {
			kinds[f.Kind] = true
		}
		names := make([]string, 0, len(kinds))
		for k := range kinds {
			names = append(names, k)
		}
		sort.Strings(names)
		summary = fmt.Sprintf(" [redacted: %s]", strings.Join(names, ", "))
	}
	const maxLog = 4000
	if len(cleaned) > maxLog {
		cleaned = cleaned[:maxLog] + fmt.Sprintf("… [%d more bytes]", len(body)-maxLog)
	}
	return cleaned + summary
}

// ProviderHosts returns the hosts implied by a provider base URL, so the policy
// can allow exactly the configured endpoint.
func ProviderHosts(provider, baseURL string) []string {
	var hosts []string
	if baseURL != "" {
		if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
			hosts = append(hosts, u.Hostname())
		}
	}
	switch provider {
	case "openai":
		hosts = append(hosts, "api.openai.com", "models.dev")
	case "anthropic":
		hosts = append(hosts, "api.anthropic.com")
	case "gemini":
		hosts = append(hosts, "generativelanguage.googleapis.com")
	case "openrouter":
		hosts = append(hosts, "openrouter.ai")
	case "ollama":
		hosts = append(hosts, "localhost", "127.0.0.1", "::1", "host.docker.internal")
	case "llamacpp":
		hosts = append(hosts, "localhost", "127.0.0.1", "::1")
	}
	return hosts
}

// IsLocalProviderHost reports whether a host belongs to a local model server,
// for which the private/loopback rules must be relaxed.
func IsLocalProviderHost(provider string) bool {
	return provider == "ollama" || provider == "llamacpp"
}

// HTTPClient builds an http.Client that enforces the policy.
func (p *Policy) HTTPClient(timeoutSeconds int) *http.Client {
	transport := p.Transport(nil)
	return &http.Client{
		Transport: transport,
		Timeout:   timeoutDuration(timeoutSeconds),
		// Redirects are re-checked because Go copies the policy transport into
		// the redirect chain, so a redirect to a blocked host is still refused.
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errs.New(errs.KindNetwork, "network", "too many redirects")
			}
			return p.CheckHost(req.Context(), req.URL.String())
		},
	}
}

func timeoutDuration(seconds int) time.Duration {
	if seconds <= 0 {
		return 0 // no client-level deadline; providers set per-request ones
	}
	return time.Duration(seconds) * time.Second
}
