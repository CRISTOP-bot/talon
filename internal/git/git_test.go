package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newRepo(t *testing.T) (*Client, string) {
	t.Helper()
	if !Available() {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	c := New(dir)
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("init", "-q")
	run("add", ".")
	run("commit", "-qm", "initial commit")
	return c, dir
}

func TestIsRepoAndTopLevel(t *testing.T) {
	c, dir := newRepo(t)
	if !c.IsRepo(context.Background()) {
		t.Fatal("IsRepo = false")
	}
	top, err := c.TopLevel(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if top == "" {
		t.Error("TopLevel is empty")
	}
	if root := RepoRoot(context.Background(), dir); root == "" {
		t.Error("RepoRoot is empty inside a repository")
	}
	plain := t.TempDir()
	if RepoRoot(context.Background(), plain) != "" {
		t.Error("RepoRoot should be empty outside a repository")
	}
	if New(plain).IsRepo(context.Background()) {
		t.Error("IsRepo should be false outside a repository")
	}
}

func TestStatusAndDiff(t *testing.T) {
	c, dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{}
	for _, f := range files {
		labels[f.Path] = f.Label()
	}
	if labels["a.txt"] != "modified" {
		t.Errorf("a.txt = %q", labels["a.txt"])
	}
	if labels["new.txt"] != "untracked" {
		t.Errorf("new.txt = %q", labels["new.txt"])
	}
	mod, added, deleted, untracked, err := c.StatusSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if mod != 1 || untracked != 1 || added != 0 || deleted != 0 {
		t.Errorf("summary = %d/%d/%d/%d", mod, added, deleted, untracked)
	}
	diff, err := c.Diff(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "-hello") || !strings.Contains(diff, "+changed") {
		t.Errorf("diff = %q", diff)
	}
	scoped, err := c.Diff(context.Background(), false, "a.txt")
	if err != nil || !strings.Contains(scoped, "changed") {
		t.Errorf("scoped diff = %q (%v)", scoped, err)
	}
	stat, err := c.DiffStat(context.Background(), false)
	if err != nil || !strings.Contains(stat, "a.txt") {
		t.Errorf("stat = %q (%v)", stat, err)
	}
}

func TestAddCommitAndLog(t *testing.T) {
	c, dir := newRepo(t)
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("second\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(context.Background(), "b.txt"); err != nil {
		t.Fatal(err)
	}
	if err := c.Commit(context.Background(), "add b", false); err != nil {
		t.Fatal(err)
	}
	commits, err := c.Log(context.Background(), LogOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("commits = %+v", commits)
	}
	if !strings.Contains(commits[0].Subject, "add b") {
		t.Errorf("newest commit = %+v", commits[0])
	}
	if commits[0].Short() == "" {
		t.Error("Short() is empty")
	}
	oneline, err := c.Log(context.Background(), LogOptions{Oneline: true, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(oneline) != 1 || oneline[0].Subject == "" {
		t.Errorf("oneline = %+v", oneline)
	}
	if err := c.Commit(context.Background(), "   ", false); err == nil {
		t.Error("an empty commit message must be rejected")
	}
	if err := c.Add(context.Background()); err == nil {
		t.Error("Add without paths must be rejected")
	}
	info, err := c.LastCommit(context.Background(), "b.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(info.Subject, "add b") {
		t.Errorf("last commit = %+v", info)
	}
}

func TestBranchesAndCheckout(t *testing.T) {
	c, _ := newRepo(t)
	branch, err := c.CurrentBranch(context.Background())
	if err != nil || branch == "" {
		t.Fatalf("branch = %q (%v)", branch, err)
	}
	if err := c.Checkout(context.Background(), "feature", true); err != nil {
		t.Fatal(err)
	}
	current, _ := c.CurrentBranch(context.Background())
	if current != "feature" {
		t.Errorf("current = %q", current)
	}
	branches, current, err := c.Branches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 2 || current != "feature" {
		t.Errorf("branches = %v, current = %q", branches, current)
	}
	if err := c.Checkout(context.Background(), "nope-not-here", false); err == nil {
		t.Error("checking out a missing branch should fail")
	}
}

func TestErrorsOutsideRepository(t *testing.T) {
	c := New(t.TempDir())
	ctx := context.Background()
	if _, err := c.Status(ctx); err == nil {
		t.Error("Status should fail outside a repository")
	}
	if _, err := c.Log(ctx, LogOptions{}); err == nil {
		t.Error("Log should fail outside a repository")
	}
	if _, err := c.CurrentBranch(ctx); err == nil {
		t.Error("CurrentBranch should fail outside a repository")
	}
}

func TestVersion(t *testing.T) {
	if !Available() {
		t.Skip("git is not installed")
	}
	v, err := Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(v, "git version") {
		t.Errorf("version = %q", v)
	}
}
