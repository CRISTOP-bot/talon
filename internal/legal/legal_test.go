package legal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTermsAreEmbeddedAndVersioned(t *testing.T) {
	docs := Terms()
	if len(docs) < 2 {
		t.Fatalf("expected a version history, got %d documents", len(docs))
	}
	if docs[0].Version != "v1.0.0" {
		t.Errorf("oldest terms = %q", docs[0].Version)
	}
	current, err := CurrentTerms()
	if err != nil {
		t.Fatal(err)
	}
	if current.Version != docs[len(docs)-1].Version {
		t.Errorf("current = %q, newest = %q", current.Version, docs[len(docs)-1].Version)
	}
	if current.EffectiveAt == "" {
		t.Error("effective date not parsed")
	}
	if current.SHA256 == "" || len(current.SHA256) != 64 {
		t.Errorf("checksum missing: %q", current.SHA256)
	}
	if !current.Material {
		t.Error("v1.1.0 is declared material but was not parsed as such")
	}
}

func TestPrivacyNoticeIsEmbedded(t *testing.T) {
	current, err := CurrentPrivacy()
	if err != nil {
		t.Fatal(err)
	}
	if current.Version == "" {
		t.Fatal("no privacy version")
	}
	// The notice must actually answer the questions it claims to.
	for _, want := range []string{
		"no telemetry", "api key", "deletion", "third party",
		"retained until you delete", "model provider",
	} {
		if !strings.Contains(strings.ToLower(current.Body), strings.ToLower(want)) {
			t.Errorf("privacy notice does not mention %q", want)
		}
	}
}

func TestTermsByVersion(t *testing.T) {
	doc, err := TermsByVersion("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(doc.Body, "version 1.0.0") {
		t.Error("wrong document returned")
	}
	if _, err := TermsByVersion("v9.9.9"); err == nil {
		t.Error("unknown version should error")
	}
	if _, err := TermsByVersion("bogus"); err == nil {
		t.Error("unknown kind/version should error")
	}
}

func TestVersionsAreOrdered(t *testing.T) {
	got := Versions(KindTerms)
	if len(got) < 2 || got[0] == "v1.0.0" {
		t.Errorf("Versions should be newest first: %v", got)
	}
}

func TestLedgerRecordsAcceptanceOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legal", "terms.json")
	l, err := OpenLedger(path)
	if err != nil {
		t.Fatal(err)
	}
	doc, _ := CurrentTerms()
	rec, err := l.Accept(doc, "cli")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Version != doc.Version || rec.SHA256 != doc.SHA256 {
		t.Errorf("record = %+v", rec)
	}
	if _, err := l.Accept(doc, "cli"); err != nil {
		t.Fatal(err)
	}
	if len(l.History(KindTerms)) != 1 {
		t.Errorf("duplicate acceptance recorded: %+v", l.History(KindTerms))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("ledger permissions = %v, want 0600", info.Mode().Perm())
	}
	dirInfo, _ := os.Stat(filepath.Dir(path))
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("ledger directory = %v, want 0700", dirInfo.Mode().Perm())
	}
}

func TestLedgerIsAppendOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terms.json")
	l, _ := OpenLedger(path)
	old, _ := TermsByVersion("v1.0.0")
	newer, _ := CurrentTerms()
	if _, err := l.Accept(old, "cli"); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Accept(newer, "cli"); err != nil {
		t.Fatal(err)
	}
	history := l.History(KindTerms)
	if len(history) != 2 {
		t.Fatalf("history = %+v", history)
	}
	if history[0].Version != "v1.0.0" || history[1].Version != "v1.1.0" {
		t.Errorf("history not chronological: %+v", history)
	}
	reopened, _ := OpenLedger(path)
	if len(reopened.History(KindTerms)) != 2 {
		t.Error("reopening the ledger lost history")
	}
}

func TestCheckRequiresConsentForNewMaterialVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terms.json")
	l, _ := OpenLedger(path)

	st, err := l.Check(KindTerms)
	if err != nil {
		t.Fatal(err)
	}
	if !st.NeedsConsent {
		t.Error("a fresh install must require acceptance")
	}
	if !strings.Contains(st.Reason, "not accepted") {
		t.Errorf("reason = %q", st.Reason)
	}

	old, _ := TermsByVersion("v1.0.0")
	if _, err := l.Accept(old, "cli"); err != nil {
		t.Fatal(err)
	}
	st, err = l.Check(KindTerms)
	if err != nil {
		t.Fatal(err)
	}
	if !st.NeedsConsent {
		t.Fatal("a material version bump must require a new acceptance")
	}
	if !strings.Contains(st.Reason, "v1.1.0") || !strings.Contains(st.Reason, "v1.0.0") {
		t.Errorf("reason should name both versions: %q", st.Reason)
	}
	if st.Changes == "" {
		t.Error("the user should be told what changed")
	}

	newer, _ := CurrentTerms()
	if _, err := l.Accept(newer, "cli"); err != nil {
		t.Fatal(err)
	}
	st, _ = l.Check(KindTerms)
	if st.NeedsConsent {
		t.Errorf("consent should be satisfied: %+v", st)
	}
	if st.Accepted != newer.Version {
		t.Errorf("accepted = %q", st.Accepted)
	}
}

func TestSameVersionNeverRequiresConsentTwice(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terms.json")
	l, _ := OpenLedger(path)
	current, _ := CurrentTerms()
	if _, err := l.Accept(current, "cli"); err != nil {
		t.Fatal(err)
	}
	st, _ := l.Check(KindTerms)
	if st.NeedsConsent {
		t.Error("the same version must not require consent again")
	}
	// Re-accepting is idempotent.
	if _, err := l.Accept(current, "cli"); err != nil {
		t.Fatal(err)
	}
	if len(l.History(KindTerms)) != 1 {
		t.Errorf("history grew on a repeat acceptance: %+v", l.History(KindTerms))
	}
}

func TestPrivacyConsentTrackedSeparately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legal.json")
	l, _ := OpenLedger(path)
	termsDoc, _ := CurrentTerms()
	privDoc, _ := CurrentPrivacy()
	_, _ = l.Accept(termsDoc, "cli")

	st, _ := l.Check(KindPrivacy)
	if !st.NeedsConsent {
		t.Error("accepting the terms must not imply accepting the privacy notice")
	}
	_, _ = l.Accept(privDoc, "cli")
	st, _ = l.Check(KindPrivacy)
	if st.NeedsConsent {
		t.Error("privacy consent should be satisfied")
	}
	if len(l.All()) != 2 {
		t.Errorf("ledger = %+v", l.All())
	}
}

func TestCorruptLedgerIsNotSilentlyReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terms.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	l, _ := OpenLedger(path)
	all := l.All()
	if len(all) == 0 {
		t.Fatal("a corrupt ledger must surface, not vanish")
	}
	if all[0].Kind != "corrupt" {
		t.Errorf("ledger = %+v", all)
	}
	if _, ok := l.Accepted(KindTerms); ok {
		t.Error("a corrupt ledger must not look like consent")
	}
}

func TestChangesSince(t *testing.T) {
	first, err := ChangesSince(KindTerms, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "v1.0.0") || !strings.Contains(first, "v1.1.0") {
		t.Errorf("changes = %q", first)
	}
	if !strings.Contains(first, "Sections that changed") {
		t.Errorf("changes should list sections: %q", first)
	}
	if _, err := ChangesSince(KindTerms, "v0.0.1"); err == nil {
		t.Error("unknown version should error")
	}
	if first, _ := ChangesSince(KindTerms, ""); !strings.Contains(first, "first version") {
		t.Errorf("no previous version = %q", first)
	}
}

func TestRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "terms.json")
	l, _ := OpenLedger(path)
	if err := l.Remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("ledger not removed")
	}
	if err := l.Remove(); err != nil {
		t.Errorf("removing twice should be a no-op: %v", err)
	}
}

func TestSummaryLine(t *testing.T) {
	cases := []Status{
		{Kind: "terms", Current: "v1.1.0"},
		{Kind: "terms", Current: "v1.1.0", Accepted: "v1.1.0", AcceptedAt: time.Now()},
		{Kind: "terms", Current: "v1.1.0", Accepted: "v1.0.0", NeedsConsent: true},
	}
	for _, c := range cases {
		if c.SummaryLine() == "" {
			t.Errorf("empty summary for %+v", c)
		}
	}
}

func TestAcceptRejectsEmptyDocument(t *testing.T) {
	l, _ := OpenLedger(filepath.Join(t.TempDir(), "t.json"))
	if _, err := l.Accept(Document{}, "cli"); err == nil {
		t.Error("an empty version must be rejected")
	}
	if _, err := OpenLedger(""); err == nil {
		t.Error("an empty ledger path must be rejected")
	}
}

func TestUnknownKindIsRejected(t *testing.T) {
	l, _ := OpenLedger(filepath.Join(t.TempDir(), "t.json"))
	if _, err := l.Check("nonsense"); err == nil {
		t.Error("an unknown document kind must be rejected")
	}
}
