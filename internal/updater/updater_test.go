package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const fakeAsset = "talon-linux-amd64"

// newTestServer serves a release API plus the assets.
func newTestServer(t *testing.T, version string, body []byte) *httptest.Server {
	t.Helper()
	// Assets are served from the same test server, so URLs must be absolute
	// once the server address is known.
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/repos/CRISTOP-bot/talon/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		rel := Release{TagName: "v" + version, Name: "Talon " + version, Assets: []Asset{
			{Name: fakeAsset, URL: base + "/" + fakeAsset},
			{Name: "checksums.txt", URL: base + "/checksums.txt"},
		}}
		json.NewEncoder(w).Encode(rel)
	})
	mux.HandleFunc("/repos/CRISTOP-bot/talon/releases/tags/v9.9.9", func(w http.ResponseWriter, r *http.Request) {
		rel := Release{TagName: "v9.9.9", Assets: []Asset{{Name: fakeAsset, URL: base + "/" + fakeAsset}}}
		json.NewEncoder(w).Encode(rel)
	})
	mux.HandleFunc("/"+fakeAsset, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	})
	mux.HandleFunc("/checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		sum := sha256.Sum256(body)
		fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), fakeAsset)
	})
	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func TestLatestParsesRelease(t *testing.T) {
	srv := newTestServer(t, "1.2.0", []byte("data"))
	u := &Updater{Repo: "CRISTOP-bot/talon", APIBase: srv.URL, HTTP: srv.Client()}
	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rel.TagName != "v1.2.0" {
		t.Errorf("tag = %q", rel.TagName)
	}
	asset, ok := u.AssetFor(rel)
	if !ok {
		t.Fatal("no asset found for this platform")
	}
	if asset.Name != fakeAsset {
		t.Errorf("asset = %q", asset.Name)
	}
	if _, ok := u.Checksums(rel); !ok {
		t.Error("checksum asset not detected")
	}
}

func TestLatestHandlesMissingReleases(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	u := &Updater{Repo: "CRISTOP-bot/talon", APIBase: srv.URL, HTTP: srv.Client()}
	if _, err := u.Latest(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDownloadAndVerify(t *testing.T) {
	body := []byte("binary-content")
	srv := newTestServer(t, "2.0.0", body)
	u := &Updater{Repo: "CRISTOP-bot/talon", APIBase: srv.URL, HTTP: srv.Client()}
	rel, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	asset, _ := u.AssetFor(rel)
	data, err := u.Download(context.Background(), asset)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(body) {
		t.Errorf("downloaded %q", data)
	}
	sumAsset, _ := u.Checksums(rel)
	sums, err := u.Download(context.Background(), sumAsset)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(data, sums, asset.Name); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestVerifyRejectsMismatchedChecksum(t *testing.T) {
	data := []byte("binary-content")
	wrong := []byte("0000000000000000000000000000000000000000000000000000000000000000  " + fakeAsset + "\n")
	if err := Verify(data, wrong, fakeAsset); err == nil {
		t.Fatal("expected a mismatch error")
	}
}

func TestVerifyRequiresAChecksumEntry(t *testing.T) {
	err := Verify([]byte("x"), []byte("something else\n"), fakeAsset)
	if err == nil {
		t.Fatal("an unverified binary must be refused")
	}
	if !strings.Contains(err.Error(), "refusing") {
		t.Errorf("error = %v", err)
	}
}

func TestVerifyAcceptsJSONChecksums(t *testing.T) {
	data := []byte("hello")
	sum := sha256.Sum256(data)
	checks := []byte(fmt.Sprintf(`{"%s": "%s"}`, fakeAsset, hex.EncodeToString(sum[:])))
	if err := Verify(data, checks, fakeAsset); err != nil {
		t.Errorf("Verify: %v", err)
	}
}

func TestInstallReplacesBinaryAndKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "talon")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Install([]byte("new"), target); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("target = %q", got)
	}
	backup, err := os.ReadFile(target + ".old")
	if err != nil || string(backup) != "old" {
		t.Errorf("backup = %q (%v)", backup, err)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the new binary is not executable: %v", info.Mode())
	}
}

func TestInstallIntoMissingDirectoryFails(t *testing.T) {
	err := Install([]byte("x"), filepath.Join(t.TempDir(), "nope", "talon"))
	if err == nil {
		t.Fatal("expected an error when the directory does not exist")
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.3", "1.2.3", 0},
		{"v1.2.3", "1.2.3", 0},
		{"1.3.0", "1.2.9", 1},
		{"1.2.0", "1.10.0", -1},
		{"2.0.0", "1.99.99", 1},
		{"1.2.3-rc1", "1.2.3", 0},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestAssetForSkipsChecksums(t *testing.T) {
	rel := &Release{Assets: []Asset{
		{Name: "talon-linux-amd64.sha256"},
		{Name: "checksums.txt"},
	}}
	u := &Updater{}
	if _, ok := u.AssetFor(rel); ok {
		t.Error("checksum files must not be selected as the binary")
	}
}

func TestDownloadFailsOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	u := &Updater{HTTP: srv.Client()}
	if _, err := u.Download(context.Background(), Asset{URL: srv.URL}); err == nil {
		t.Fatal("expected an error")
	}
}

// TestAssetForIgnoresCompanionBinaries is the regression test for an update that
// installed talon-mcp in place of talon: every release ships several binaries and
// a substring match on "linux" and "amd64" cannot tell them apart.
func TestAssetForIgnoresCompanionBinaries(t *testing.T) {
	rel := &Release{
		TagName: "v9.9.9",
		Assets: []Asset{
			{Name: "talon-mcp-v9.9.9-linux-amd64"},
			{Name: "talon-mock-provider-v9.9.9-linux-amd64"},
			{Name: "talon-sandbox-v9.9.9-linux-amd64"},
			{Name: "talon-v9.9.9-linux-amd64"},
			{Name: "checksums.txt"},
		},
	}
	u := &Updater{}
	got, ok := u.AssetFor(rel)
	if !ok {
		t.Fatal("no asset was chosen for this platform")
	}
	if got.Name != AssetName("talon", "v9.9.9", "linux", "amd64") {
		t.Fatalf("chose %q, want the talon binary", got.Name)
	}
}

func TestAssetNameMatchesTheReleaseNaming(t *testing.T) {
	if got := AssetName("talon", "v1.0.0", "linux", "amd64"); got != "talon-v1.0.0-linux-amd64" {
		t.Errorf("AssetName = %q", got)
	}
	if got := AssetName("talon", "v1.0.0", "windows", "arm64"); got != "talon-v1.0.0-windows-arm64.exe" {
		t.Errorf("AssetName = %q", got)
	}
}

// TestAssetForRefusesToGuess proves that an ambiguous release is refused rather
// than resolved to an arbitrary binary.
func TestAssetForRefusesToGuess(t *testing.T) {
	rel := &Release{
		TagName: "v9.9.9",
		Assets: []Asset{
			{Name: "talon-linux-amd64"},
			{Name: "talon-mcp-linux-amd64"},
		},
	}
	u := &Updater{}
	if _, ok := u.AssetFor(rel); ok {
		t.Fatal("an ambiguous release must not resolve to an arbitrary asset")
	}
}
