package legal

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot walks up from the test's directory to the module root.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("could not find the module root")
	return ""
}

// TestRootDocumentsMatchEmbeddedAssets proves the published Terms and Privacy
// documents are the same text the binary serves. Drift between them would mean
// the repository advertises terms the program does not enforce.
func TestRootDocumentsMatchEmbeddedAssets(t *testing.T) {
	root := repoRoot(t)
	cases := []struct {
		file    string
		version string
	}{
		{"TERMS.md", "v1.1.0"},
		{"PRIVACY.md", "v1.0.0"},
	}
	for _, c := range cases {
		data, err := os.ReadFile(filepath.Join(root, c.file))
		if err != nil {
			t.Fatalf("%s must exist at the repository root: %v", c.file, err)
		}
		published := string(data)
		kind := KindTerms
		if c.file == "PRIVACY.md" {
			kind = KindPrivacy
		}
		doc := mustLoad(kind)[0]
		for _, d := range mustLoad(kind) {
			if d.Version == c.version {
				doc = d
			}
		}
		if doc.Version != c.version {
			t.Fatalf("%s should mirror %s, found %s", c.file, c.version, doc.Version)
		}
		body := strings.TrimSpace(doc.Body)
		if !strings.Contains(published, body) {
			t.Fatalf("%s does not contain the embedded %s %s verbatim; "+
				"regenerate it from internal/legal/assets", c.file, kind, doc.Version)
		}
	}
}

// TestTermsVersionChainIsOrdered proves the version list increases, so
// CurrentTerms really returns the newest document.
func TestTermsVersionChainIsOrdered(t *testing.T) {
	docs := mustLoad(KindTerms)
	if len(docs) < 2 {
		t.Skip("only one Terms version is embedded")
	}
	for i := 1; i < len(docs); i++ {
		if !lessVersion(docs[i-1].Version, docs[i].Version) {
			t.Fatalf("version %s should sort before %s", docs[i-1].Version, docs[i].Version)
		}
	}
	latest := docs[len(docs)-1]
	if current, err := CurrentTerms(); err != nil || current.Version != latest.Version {
		t.Fatalf("CurrentTerms returned %+v, want %s (err %v)", current, latest.Version, err)
	}
}

// TestOnlyMaterialVersionsRequireConsent proves editorial changes do not nag.
func TestOnlyMaterialVersionsRequireConsent(t *testing.T) {
	docs := mustLoad(KindTerms)
	for _, d := range docs {
		if d.Version == "v1.0.0" && d.Material {
			t.Fatal("the initial release does not count as a material change")
		}
	}
	latest := docs[len(docs)-1]
	if !latest.Material {
		t.Fatal("the current Terms must be marked material so acceptance is requested")
	}
}
