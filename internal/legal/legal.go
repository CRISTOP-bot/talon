// Package legal serves the versioned Terms of Use and Privacy Notice, and keeps
// a local record of the version each user accepted.
//
// Rules this package enforces:
//
//   - Terms are immutable. A change creates a new versioned document; nothing is
//     ever overwritten, so the history stays auditable.
//   - Acceptance is explicit. Talon records consent only when the user accepts,
//     and a new material version is never accepted on the user's behalf.
//   - Consent is append-only. Previous acceptances remain on disk.
//   - Nothing is uploaded. The ledger is a local file with owner-only
//     permissions.
package legal

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/talon-cli/talon/internal/errs"
)

//go:embed assets/terms/*.md assets/privacy/*.md
var assets embed.FS

// Document is a versioned legal text.
type Document struct {
	Kind        string `json:"kind"`
	Version     string `json:"version"`
	EffectiveAt string `json:"effective_at"`
	Material    bool   `json:"material"`
	Body        string `json:"-"`
	SHA256      string `json:"sha256"`
}

// Kind identifies which document this is.
const (
	KindTerms   = "terms"
	KindPrivacy = "privacy"
)

var versionHeader = regexp.MustCompile(`\*\*Effective date:\*\*\s*(\S+)`)
var materialHeader = regexp.MustCompile(`\*\*Material change:\*\*\s*yes`)

func loadDir(kind string) ([]Document, error) {
	entries, err := fs.ReadDir(assets, "assets/"+kind)
	if err != nil {
		return nil, errs.Internal("legal", "cannot read embedded %s: %v", kind, err)
	}
	var docs []Document
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}
		version := strings.TrimSuffix(name, ".md")
		body, err := assets.ReadFile("assets/" + kind + "/" + name)
		if err != nil {
			return nil, errs.Internal("legal", "cannot read embedded %s: %v", name, err)
		}
		text := string(body)
		effective := ""
		if m := versionHeader.FindStringSubmatch(text); len(m) == 2 {
			effective = m[1]
		}
		sum := sha256.Sum256(body)
		docs = append(docs, Document{
			Kind:        kind,
			Version:     version,
			EffectiveAt: effective,
			Material:    materialHeader.MatchString(text),
			Body:        text,
			SHA256:      hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(docs, func(i, j int) bool { return lessVersion(docs[i].Version, docs[j].Version) })
	return docs, nil
}

func lessVersion(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		av, bv := 0, 0
		if i < len(as) {
			fmt.Sscanf(as[i], "%d", &av)
		}
		if i < len(bs) {
			fmt.Sscanf(bs[i], "%d", &bv)
		}
		if av != bv {
			return av < bv
		}
	}
	return false
}

// Terms returns every version of the Terms of Use, oldest first.
func Terms() []Document { return mustLoad(KindTerms) }

// Privacy returns every version of the Privacy Notice, oldest first.
func Privacy() []Document { return mustLoad(KindPrivacy) }

func mustLoad(kind string) []Document {
	docs, err := loadDir(kind)
	if err != nil {
		return nil
	}
	return docs
}

// CurrentTerms returns the newest Terms version.
func CurrentTerms() (Document, error) {
	docs := Terms()
	if len(docs) == 0 {
		return Document{}, errs.NotFound("legal", "no terms are embedded in this build")
	}
	return docs[len(docs)-1], nil
}

// CurrentPrivacy returns the newest Privacy Notice version.
func CurrentPrivacy() (Document, error) {
	docs := Privacy()
	if len(docs) == 0 {
		return Document{}, errs.NotFound("legal", "no privacy notice is embedded in this build")
	}
	return docs[len(docs)-1], nil
}

// DocumentByKind returns the newest version of a document kind.
func DocumentByKind(kind string) (Document, error) {
	switch kind {
	case KindTerms:
		return CurrentTerms()
	case KindPrivacy:
		return CurrentPrivacy()
	}
	return Document{}, errs.NotFound("legal", "unknown document kind %q", kind)
}

// TermsByVersion returns a specific Terms version.
func TermsByVersion(v string) (Document, error) { return byVersion(KindTerms, v) }

// PrivacyByVersion returns a specific Privacy Notice version.
func PrivacyByVersion(v string) (Document, error) { return byVersion(KindPrivacy, v) }

func byVersion(kind, v string) (Document, error) {
	for _, d := range mustLoad(kind) {
		if d.Version == v || d.Version == "v"+v {
			return d, nil
		}
	}
	return Document{}, errs.NotFound("legal", "no %s version %q is available (have: %s)",
		kind, v, versionList(kind))
}

func versionList(kind string) string {
	docs := mustLoad(kind)
	names := make([]string, 0, len(docs))
	for _, d := range docs {
		names = append(names, d.Version)
	}
	return strings.Join(names, ", ")
}

// Versions lists the available versions of a document kind, newest first.
func Versions(kind string) []string {
	docs := mustLoad(kind)
	names := make([]string, 0, len(docs))
	for i := len(docs) - 1; i >= 0; i-- {
		names = append(names, docs[i].Version)
	}
	return names
}

// ChangesSince renders what changed between two versions: the version list and
// the headings added in the newer document. It never invents a diff.
func ChangesSince(kind, from string) (string, error) {
	docs := mustLoad(kind)
	if len(docs) == 0 {
		return "", errs.NotFound("legal", "no %s available", kind)
	}
	latest := docs[len(docs)-1]
	if from == "" {
		return "This is the first version of this document available in this build.", nil
	}
	var previous *Document
	for i := range docs {
		if docs[i].Version == from {
			previous = &docs[i]
			break
		}
	}
	if previous == nil {
		return "", errs.NotFound("legal", "version %q is not available (have: %s)", from, versionList(kind))
	}
	oldHeadings := map[string]bool{}
	for _, h := range headings(previous.Body) {
		oldHeadings[h] = true
	}
	var added []string
	for _, h := range headings(latest.Body) {
		if !oldHeadings[h] {
			added = append(added, h)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Changed from %s to %s (effective %s).\n\n", previous.Version, latest.Version, latest.EffectiveAt)
	if len(added) == 0 {
		b.WriteString("Sections are unchanged; the wording was revised. Read the full document.\n")
		return b.String(), nil
	}
	b.WriteString("Sections that changed:\n")
	for _, h := range added {
		fmt.Fprintf(&b, "  • %s\n", h)
	}
	b.WriteString("\nRead the full text with: talon terms --current\n")
	return b.String(), nil
}

func headings(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "## ") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "## ")))
		}
	}
	return out
}

// Acceptance is one recorded consent.
type Acceptance struct {
	Kind       string    `json:"kind"`
	Version    string    `json:"version"`
	AcceptedAt time.Time `json:"accepted_at"`
	SHA256     string    `json:"sha256"`
	// Source records how consent was given: "cli" for an interactive
	// acceptance, "flag" for --accept-terms, "test" for automated runs.
	Source string `json:"source"`
}

// ledger is the on-disk consent record.
type ledger struct {
	Acceptances []Acceptance `json:"acceptances"`
}

// Ledger is the append-only consent store.
type Ledger struct {
	path string
	mu   sync.Mutex
}

// OpenLedger opens (or creates) the consent ledger at path.
func OpenLedger(path string) (*Ledger, error) {
	if path == "" {
		return nil, errs.Usage("no consent ledger path configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, errs.Wrap(errs.KindPermission, "legal", err)
	}
	return &Ledger{path: path}, nil
}

// Path returns the ledger path.
func (l *Ledger) Path() string { return l.path }

// Accept records consent for a document version. Recording is explicit: no code
// path calls this without a user action behind it.
func (l *Ledger) Accept(doc Document, source string) (Acceptance, error) {
	if doc.Version == "" {
		return Acceptance{}, errs.Usage("cannot accept an empty document version")
	}
	if source == "" {
		source = "unknown"
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	existing := l.readLocked()
	for _, a := range existing.Acceptances {
		if a.Kind == doc.Kind && a.Version == doc.Version && a.SHA256 == doc.SHA256 {
			return a, nil // already accepted: do not duplicate history
		}
	}
	rec := Acceptance{
		Kind:       doc.Kind,
		Version:    doc.Version,
		AcceptedAt: time.Now(),
		SHA256:     doc.SHA256,
		Source:     source,
	}
	existing.Acceptances = append(existing.Acceptances, rec)
	if err := l.writeLocked(existing); err != nil {
		return Acceptance{}, err
	}
	return rec, nil
}

// Accepted returns the most recent acceptance for a document kind.
func (l *Ledger) Accepted(kind string) (Acceptance, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	var latest Acceptance
	found := false
	for _, a := range l.readLocked().Acceptances {
		if a.Kind != kind {
			continue
		}
		if !found || a.AcceptedAt.After(latest.AcceptedAt) {
			latest = a
			found = true
		}
	}
	return latest, found
}

// History returns every acceptance for a kind, oldest first.
func (l *Ledger) History(kind string) []Acceptance {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []Acceptance
	for _, a := range l.readLocked().Acceptances {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].AcceptedAt.Before(out[j].AcceptedAt) })
	return out
}

// All returns every acceptance record.
func (l *Ledger) All() []Acceptance {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Acceptance(nil), l.readLocked().Acceptances...)
}

// Status describes whether consent is current.
type Status struct {
	Kind string
	// Current is the version in force in this build.
	Current string
	// Accepted is the version the user accepted, if any.
	Accepted string
	// AcceptedAt is when consent was given.
	AcceptedAt time.Time
	// NeedsConsent is true when a newer version exists that the user has not
	// accepted. Only material changes set it.
	NeedsConsent bool
	// Reason explains why consent is needed.
	Reason string
	// Changes summarises what changed.
	Changes string
}

// Check returns the consent status for a document kind without changing it.
func (l *Ledger) Check(kind string) (Status, error) {
	var current Document
	var err error
	docs := mustLoad(kind)
	switch kind {
	case KindTerms:
		current, err = CurrentTerms()
	case KindPrivacy:
		current, err = CurrentPrivacy()
	default:
		return Status{}, errs.Usage("unknown document kind %q", kind)
	}
	if err != nil {
		return Status{}, err
	}
	st := Status{Kind: kind, Current: current.Version}
	accepted, ok := l.Accepted(kind)
	if !ok {
		st.NeedsConsent = true
		st.Reason = fmt.Sprintf("you have not accepted %s version %s yet", kind, current.Version)
		if len(docs) <= 1 {
			st.Changes = "No earlier version of this document is available in this build, " +
				"so there is nothing to compare it against."
		} else {
			st.Changes, _ = ChangesSince(kind, docs[0].Version)
		}
		return st, nil
	}
	st.Accepted = accepted.Version
	st.AcceptedAt = accepted.AcceptedAt
	if accepted.Version == current.Version {
		return st, nil
	}
	// A newer version exists. Consent is required only when the new version is
	// marked material; editorial changes are reported but not blocking.
	if current.Material {
		st.NeedsConsent = true
		st.Reason = fmt.Sprintf(
			"%s version %s contains material changes and you accepted %s", kind, current.Version, accepted.Version)
		changes, cerr := ChangesSince(kind, accepted.Version)
		if cerr == nil {
			st.Changes = changes
		}
	}
	return st, nil
}

func (l *Ledger) readLocked() ledger {
	var data ledger
	raw, err := os.ReadFile(l.path)
	if err != nil {
		return ledger{}
	}
	if err := json.Unmarshal(raw, &data); err != nil {
		// A corrupt ledger must not be silently replaced: it is evidence.
		return ledger{Acceptances: []Acceptance{{
			Kind: "corrupt", Version: l.path, AcceptedAt: time.Now(), Source: "unreadable",
		}}}
	}
	return data
}

func (l *Ledger) writeLocked(data ledger) error {
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return errs.Internal("legal", "cannot encode the consent ledger: %v", err)
	}
	if err := os.WriteFile(l.path, append(encoded, '\n'), 0o600); err != nil {
		return errs.Wrap(errs.KindPermission, "legal", err)
	}
	return nil
}

// Remove deletes the ledger (used by `talon data clear`).
func (l *Ledger) Remove() error {
	if err := os.Remove(l.path); err != nil && !os.IsNotExist(err) {
		return errs.Wrap(errs.KindPermission, "legal", err)
	}
	return nil
}

// SummaryLine renders a consent status for the terminal.
func (s Status) SummaryLine() string {
	switch {
	case !s.NeedsConsent && s.Accepted == "":
		return fmt.Sprintf("%s %s: not accepted yet", s.Kind, s.Current)
	case !s.NeedsConsent:
		return fmt.Sprintf("%s %s: accepted on %s", s.Kind, s.Accepted,
			s.AcceptedAt.Format("2006-01-02"))
	default:
		return fmt.Sprintf("%s: %s requires your acceptance (you accepted %s)", s.Kind, s.Current, s.Accepted)
	}
}
