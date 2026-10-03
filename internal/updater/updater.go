// Package updater implements `talon update`.
//
// The update path is deliberately conservative: it only downloads release
// assets published by the configured GitHub repository, verifies them against
// the published SHA-256 checksums when they exist, and replaces the running
// binary atomically. No remote script is ever executed.
package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/CRISTOP-bot/talon/internal/app"
	"github.com/CRISTOP-bot/talon/internal/errs"
)

// Release is a published release.
type Release struct {
	TagName string  `json:"tag_name"`
	Name    string  `json:"name"`
	Body    string  `json:"body"`
	Assets  []Asset `json:"assets"`
	Draft   bool    `json:"draft"`
	Pre     bool    `json:"pre-release"`
}

// Asset is a downloadable file attached to a release.
type Asset struct {
	Name string `json:"name"`
	URL  string `json:"browser_download_url"`
	Size int64  `json:"size"`
}

// Updater talks to the release API.
type Updater struct {
	// Repo is "owner/name".
	Repo string
	// APIBase allows tests to point at a local server.
	APIBase string
	HTTP    *http.Client
}

// New creates an updater for a repository.
func New(repo string) *Updater {
	base := "https://api.github.com"
	if v := os.Getenv("TALON_UPDATE_API"); v != "" {
		base = v
	}
	return &Updater{Repo: repo, APIBase: base, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// Latest fetches the newest published release.
func (u *Updater) Latest(ctx context.Context) (*Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(u.APIBase, "/"), u.Repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errs.Internal("update", "%s", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", app.UserAgent())
	resp, err := u.http().Do(req)
	if err != nil {
		return nil, errs.Newf(errs.KindNetwork, "update", "cannot reach the release API: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, errs.NotFound("update", "%s has no published releases", u.Repo)
	case http.StatusForbidden:
		return nil, errs.New(errs.KindRateLimit, "update",
			"the GitHub API rate limit was reached; try again later or download the release manually")
	}
	if resp.StatusCode >= 400 {
		return nil, errs.Newf(errs.KindNetwork, "update", "the release API returned HTTP %d", resp.StatusCode)
	}
	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, errs.Parse("update", "the release API returned malformed JSON: %v", err)
	}
	return &rel, nil
}

func (u *Updater) http() *http.Client {
	if u.HTTP != nil {
		return u.HTTP
	}
	return http.DefaultClient
}

// AssetFor picks the asset matching the running platform.
func (u *Updater) AssetFor(rel *Release) (Asset, bool) {
	goos := runtime.GOOS
	goarch := runtime.GOARCH
	for _, a := range rel.Assets {
		name := strings.ToLower(a.Name)
		if !strings.Contains(name, goos) || !strings.Contains(name, goarch) {
			continue
		}
		if strings.HasSuffix(name, ".sha256") || strings.HasSuffix(name, ".txt") ||
			strings.HasSuffix(name, ".asc") || strings.Contains(name, "checksum") {
			continue
		}
		return a, true
	}
	return Asset{}, false
}

// Checksums picks the checksum file of a release, if one was published.
func (u *Updater) Checksums(rel *Release) (Asset, bool) {
	for _, a := range rel.Assets {
		name := strings.ToLower(a.Name)
		if strings.Contains(name, "checksum") || strings.HasSuffix(name, ".sha256") {
			return a, true
		}
	}
	return Asset{}, false
}

// Download fetches an asset into memory, refusing absurdly large files.
func (u *Updater) Download(ctx context.Context, asset Asset) ([]byte, error) {
	const maxSize = 200 << 20
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return nil, errs.Internal("update", "%s", err)
	}
	req.Header.Set("User-Agent", app.UserAgent())
	resp, err := u.http().Do(req)
	if err != nil {
		return nil, errs.Newf(errs.KindNetwork, "update", "download failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, errs.Newf(errs.KindNetwork, "update", "download returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxSize))
	if err != nil {
		return nil, errs.Newf(errs.KindNetwork, "update", "download interrupted: %v", err)
	}
	return data, nil
}

// Verify checks a downloaded asset against a checksum file. The file may use
// either "<sha256>  <name>" or a JSON object mapping names to digests.
func Verify(data []byte, checksumFile []byte, assetName string) error {
	sum := sha256.Sum256(data)
	want := ""
	text := strings.TrimSpace(string(checksumFile))
	if strings.HasPrefix(text, "{") {
		var m map[string]string
		if err := json.Unmarshal([]byte(text), &m); err != nil {
			return errs.Parse("update", "the checksum file is not valid JSON: %v", err)
		}
		for name, digest := range m {
			if filepath.Base(name) == filepath.Base(assetName) {
				want = digest
			}
		}
	} else {
		for _, line := range strings.Split(text, "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			if filepath.Base(fields[1]) == filepath.Base(assetName) {
				want = fields[0]
			}
		}
	}
	if want == "" {
		return errs.NotFound("update",
			"the release has no checksum for %s; refusing to install an unverified binary", assetName)
	}
	if !strings.EqualFold(want, hex.EncodeToString(sum[:])) {
		return errs.Permission("update",
			"checksum mismatch for %s: expected %s, got %s", assetName, want, hex.EncodeToString(sum[:]))
	}
	return nil
}

// Install writes data to targetPath atomically, keeping a backup of the old
// binary so a broken update can be rolled back.
func Install(data []byte, targetPath string) error {
	dir := filepath.Dir(targetPath)
	tmp, err := os.CreateTemp(dir, ".talon-update-*")
	if err != nil {
		return errs.Permission("update", "cannot write to %s: %v", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return errs.Internal("update", "cannot write the new binary: %v", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return errs.Internal("update", "%s", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		_ = os.Remove(tmpName)
		return errs.Permission("update", "cannot set the executable bit: %v", err)
	}
	backup := ""
	if info, serr := os.Stat(targetPath); serr == nil && info.Mode().IsRegular() {
		backup = targetPath + ".old"
		_ = os.Remove(backup)
		if err := os.Rename(targetPath, backup); err != nil {
			_ = os.Remove(tmpName)
			return errs.Permission("update", "cannot replace the running binary: %v", err)
		}
	}
	if err := os.Rename(tmpName, targetPath); err != nil {
		if backup != "" {
			_ = os.Rename(backup, targetPath)
		}
		_ = os.Remove(tmpName)
		return errs.Permission("update", "cannot install the new binary: %v", err)
	}
	return nil
}

// CompareVersions compares dotted versions, ignoring a leading "v".
// It returns 1 when a is newer than b, -1 when older and 0 when equal.
func CompareVersions(a, b string) int {
	pa := parseVersion(a)
	pb := parseVersion(b)
	for i := 0; i < 3; i++ {
		if pa[i] > pb[i] {
			return 1
		}
		if pa[i] < pb[i] {
			return -1
		}
	}
	return 0
}

func parseVersion(v string) [3]int {
	v = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(v), "v"))
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, part := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			continue
		}
		out[i] = n
	}
	return out
}
