package privacy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/talon-cli/talon/internal/paths"
)

func tempDirs(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(paths.EnvConfigDir, filepath.Join(dir, "config"))
	t.Setenv(paths.EnvDataDir, filepath.Join(dir, "data"))
	t.Setenv(paths.EnvStateDir, filepath.Join(dir, "state"))
	t.Setenv(paths.EnvCacheDir, filepath.Join(dir, "cache"))
}

func TestInventoryFindsStoredData(t *testing.T) {
	tempDirs(t)
	sessions := filepath.Join(paths.Data(), "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, "a.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".talon"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".talon", "config.toml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	items := Inventory(project)
	byCat := map[Category]Item{}
	for _, i := range items {
		byCat[i.Category] = i
	}
	if !byCat[CatSessions].Exists || byCat[CatSessions].Files != 1 {
		t.Errorf("sessions not detected: %+v", byCat[CatSessions])
	}
	if !byCat[CatProject].Exists || byCat[CatProject].Scope != "project" {
		t.Errorf("project data not detected: %+v", byCat[CatProject])
	}
	if byCat[CatLogs].Exists {
		t.Error("logs should be absent")
	}
	files, bytes := Total(items)
	if files < 2 || bytes == 0 {
		t.Errorf("total = %d files, %d bytes", files, bytes)
	}
}

func TestItemHumanSize(t *testing.T) {
	cases := []Item{
		{Exists: false},
		{Exists: true, Files: 1, Bytes: 10},
		{Exists: true, Files: 2, Bytes: 4096},
		{Exists: true, Files: 3, Bytes: 3 << 20},
	}
	for _, c := range cases {
		if c.Human() == "" {
			t.Errorf("empty human size for %+v", c)
		}
	}
	if !strings.Contains(Item{}.Human(), "absent") {
		t.Error("absent items should say so")
	}
}

func TestBuildPlanSelectsCategories(t *testing.T) {
	tempDirs(t)
	for _, dir := range []string{
		filepath.Join(paths.Data(), "sessions"),
		filepath.Join(paths.State(), "logs"),
		filepath.Join(paths.Data(), "memory"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	plan := BuildPlan(ClearRequest{Categories: []Category{CatSessions}})
	if len(plan.Items) != 1 || plan.Items[0].Category != CatSessions {
		t.Fatalf("plan = %+v", plan.Items)
	}
	if plan.Bytes() == 0 {
		t.Error("plan size not computed")
	}

	all := BuildPlan(ClearRequest{})
	if len(all.Items) < 3 {
		t.Errorf("an empty request should cover everything present: %+v", all.Items)
	}
	// Plugins are only removed when explicitly requested.
	for _, i := range all.Items {
		if i.Category == CatPlugins {
			t.Error("plugins must not be deleted by default")
		}
	}
	withPlugins := BuildPlan(ClearRequest{Categories: []Category{CatPlugins}, IncludePlugins: true})
	_ = withPlugins
}

func TestApplyDeletesPlannedItemsOnly(t *testing.T) {
	tempDirs(t)
	keep := filepath.Join(paths.State(), "logs")
	remove := filepath.Join(paths.Data(), "sessions")
	for _, dir := range []string{keep, remove} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plan := BuildPlan(ClearRequest{Categories: []Category{CatSessions}})
	if err := Apply(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(remove); !os.IsNotExist(err) {
		t.Error("planned item was not deleted")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Error("unplanned item was deleted")
	}
}

func TestPlanReportsMissingPaths(t *testing.T) {
	tempDirs(t)
	plan := BuildPlan(ClearRequest{})
	if len(plan.Missing) == 0 {
		t.Error("absent paths should be reported, not silently skipped")
	}
	if err := Apply(plan); err != nil {
		t.Errorf("applying a plan with missing paths should succeed: %v", err)
	}
}

func TestProjectScopeRequiresRoot(t *testing.T) {
	tempDirs(t)
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".talon"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".talon", "memory.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Without a project root, project data must not be targeted.
	plan := BuildPlan(ClearRequest{})
	for _, i := range plan.Items {
		if i.Category == CatProject {
			t.Error("project data must not be deleted without a project root")
		}
	}
	plan = BuildPlan(ClearRequest{ProjectRoot: project})
	found := false
	for _, i := range plan.Items {
		if i.Category == CatProject {
			found = true
		}
	}
	if !found {
		t.Errorf("project data not planned: %+v", plan.Items)
	}
}

func TestModeNoticeIsHonest(t *testing.T) {
	m := Mode{}
	if m.Notice() != "" {
		t.Error("a disabled mode has no notice")
	}
	m = Mode{Enabled: true, Reason: "--privacy"}
	notice := m.Notice()
	for _, want := range []string{
		"privacy mode is ON", "--privacy", "disabled:", "still true:",
		"limitations:", "provider",
	} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice missing %q:\n%s", want, notice)
		}
	}
	// Every documented limitation must appear, so nothing is hidden.
	for _, l := range Limitations() {
		if !strings.Contains(notice, l[:30]) {
			t.Errorf("limitation not shown: %q", l)
		}
	}
}

func TestParseCategory(t *testing.T) {
	if c, err := ParseCategory("sessions"); err != nil || c != CatSessions {
		t.Errorf("ParseCategory(sessions) = %q %v", c, err)
	}
	if _, err := ParseCategory("nonsense"); err == nil {
		t.Error("unknown category must be rejected")
	}
	if _, err := ParseCategory(""); err == nil {
		t.Error("empty category must be rejected")
	}
	if len(Categories()) < 8 {
		t.Errorf("categories = %v", Categories())
	}
	if CatSessions.Label() == "" || CatSessions.Label() == string(CatSessions) {
		t.Errorf("label = %q", CatSessions.Label())
	}
}

func TestPathsAreDistinctFromConfig(t *testing.T) {
	tempDirs(t)
	if AuditPath() == "" || LegalPath() == "" {
		t.Fatal("paths must be defined")
	}
	if strings.HasPrefix(AuditPath(), paths.Config()) {
		t.Error("the audit log must not live in the config directory")
	}
}
