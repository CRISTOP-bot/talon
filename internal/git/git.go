// Package git wraps the git command line with a small, typed API. Talon never
// pushes automatically and always shows the user what changed.
package git

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/errs"
)

// Client runs git commands in a repository.
type Client struct {
	// Bin is the git binary; empty means "git" from PATH.
	Bin string
	// Dir is the repository directory.
	Dir string
}

// New creates a client for dir.
func New(dir string) *Client { return &Client{Dir: dir} }

// Available reports whether git exists on this machine.
func Available() bool {
	_, err := exec.LookPath("git")
	return err == nil
}

// Version returns the git version string.
func Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "--version").Output()
	if err != nil {
		return "", errs.NotFound("git", "git is not installed")
	}
	return strings.TrimSpace(string(out)), nil
}

func (c *Client) run(ctx context.Context, args ...string) (string, error) {
	bin := c.Bin
	if bin == "" {
		bin = "git"
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = c.Dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return string(out), errs.Newf(errs.KindExecution, "git", "git %s failed: %s",
			strings.Join(args, " "), msg)
	}
	return string(out), nil
}

// IsRepo reports whether the directory is inside a git work tree.
func (c *Client) IsRepo(ctx context.Context) bool {
	out, err := c.run(ctx, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// TopLevel returns the repository root.
func (c *Client) TopLevel(ctx context.Context) (string, error) {
	out, err := c.run(ctx, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// CurrentBranch returns the checked-out branch name. It works in a repository
// without commits, where HEAD does not resolve yet.
func (c *Client) CurrentBranch(ctx context.Context) (string, error) {
	if out, err := c.run(ctx, "symbolic-ref", "--short", "HEAD"); err == nil {
		if name := strings.TrimSpace(out); name != "" {
			return name, nil
		}
	}
	out, err := c.run(ctx, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// FileStatus is the state of one path in the working tree.
type FileStatus struct {
	Path   string `json:"path"`
	Staged bool   `json:"staged"`
	// X and Y are the two status characters from `git status --porcelain`.
	X string `json:"x"`
	Y string `json:"y"`
}

// Label renders a human-friendly status word.
func (s FileStatus) Label() string {
	switch {
	case s.X == "?" && s.Y == "?":
		return "untracked"
	case s.X == "A":
		return "added"
	case s.X == "M" || s.Y == "M":
		return "modified"
	case s.X == "D" || s.Y == "D":
		return "deleted"
	case s.X == "R":
		return "renamed"
	default:
		return strings.TrimSpace(s.X + s.Y)
	}
}

// Status returns the porcelain status of the working tree.
func (c *Client) Status(ctx context.Context) ([]FileStatus, error) {
	out, err := c.run(ctx, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	var files []FileStatus
	for _, line := range strings.Split(out, "\n") {
		if len(line) < 4 {
			continue
		}
		x, y := line[0], line[1]
		path := strings.TrimSpace(line[2:])
		// Renames appear as "old -> new".
		if strings.Contains(path, " -> ") {
			path = strings.Split(path, " -> ")[1]
		}
		path = strings.Trim(path, "\"")
		files = append(files, FileStatus{
			Path:   path,
			X:      string(x),
			Y:      string(y),
			Staged: x != ' ' && x != '?',
		})
	}
	return files, nil
}

// StatusSummary counts the changes by category.
func (c *Client) StatusSummary(ctx context.Context) (modified, added, deleted, untracked int, err error) {
	files, err := c.Status(ctx)
	if err != nil {
		return 0, 0, 0, 0, err
	}
	for _, f := range files {
		switch f.Label() {
		case "modified":
			modified++
		case "added":
			added++
		case "deleted":
			deleted++
		case "untracked":
			untracked++
		}
	}
	return modified, added, deleted, untracked, nil
}

// Diff returns the unified diff of the working tree, optionally restricted to
// some paths.
func (c *Client) Diff(ctx context.Context, staged bool, paths ...string) (string, error) {
	args := []string{"diff", "--no-color"}
	if staged {
		args = append(args, "--staged")
	}
	if len(paths) > 0 {
		args = append(args, "--")
		args = append(args, paths...)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return "", err
	}
	return out, nil
}

// DiffStat summarises the working tree changes per file.
func (c *Client) DiffStat(ctx context.Context, staged bool) (string, error) {
	args := []string{"diff", "--stat"}
	if staged {
		args = append(args, "--staged")
	}
	return c.run(ctx, args...)
}

// Commit is one entry of the log.
type Commit struct {
	Hash    string
	Author  string
	Date    string
	Subject string
}

// Short returns the abbreviated hash.
func (c Commit) Short() string {
	if len(c.Hash) > 8 {
		return c.Hash[:8]
	}
	return c.Hash
}

// LogOptions filters the log.
type LogOptions struct {
	Limit int
	// Path restricts the log to one file.
	Path string
	// Author filters by author substring.
	Author string
	// Since is a git date string such as "2 weeks ago".
	Since string
	// Oneline uses the compact one-line format.
	Oneline bool
}

// Log returns commits matching opts.
func (c *Client) Log(ctx context.Context, opts LogOptions) ([]Commit, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}
	args := []string{"log", "--no-color"}
	if opts.Oneline {
		args = append(args, "--oneline")
	} else {
		args = append(args, "--format=%H|%an|%ad|%s", "--date=short")
	}
	args = append(args, "-n", strconv.Itoa(limit))
	if opts.Since != "" {
		args = append(args, "--since="+opts.Since)
	}
	if opts.Author != "" {
		args = append(args, "--author="+opts.Author)
	}
	if opts.Path != "" {
		args = append(args, "--", opts.Path)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		if opts.Oneline {
			parts := strings.SplitN(line, " ", 2)
			c := Commit{Hash: parts[0]}
			if len(parts) > 1 {
				c.Subject = parts[1]
			}
			commits = append(commits, c)
			continue
		}
		// Default format: hash|author|date|subject
		fields := strings.SplitN(line, "|", 4)
		for len(fields) < 4 {
			fields = append(fields, "")
		}
		commits = append(commits, Commit{
			Hash: fields[0], Author: fields[1], Date: fields[2], Subject: fields[3],
		})
	}
	return commits, nil
}

// Branches lists local branches; the current one is marked.
func (c *Client) Branches(ctx context.Context) ([]string, string, error) {
	out, err := c.run(ctx, "branch", "--format=%(refname:short)")
	if err != nil {
		return nil, "", err
	}
	current, _ := c.CurrentBranch(ctx)
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" {
			branches = append(branches, line)
		}
	}
	return branches, current, nil
}

// Checkout switches branches (or restores paths with --).
func (c *Client) Checkout(ctx context.Context, target string, create bool) error {
	args := []string{"checkout"}
	if create {
		args = append(args, "-b")
	}
	args = append(args, target)
	_, err := c.run(ctx, args...)
	return err
}

// Branch creates a branch without switching to it.
func (c *Client) Branch(ctx context.Context, name string) error {
	_, err := c.run(ctx, "branch", name)
	return err
}

// Add stages paths.
func (c *Client) Add(ctx context.Context, paths ...string) error {
	if len(paths) == 0 {
		return errs.Usage("git_add requires at least one path")
	}
	_, err := c.run(ctx, append([]string{"add", "--"}, paths...)...)
	return err
}

// Commit creates a commit. An empty message is rejected before running git.
func (c *Client) Commit(ctx context.Context, message string, all bool) error {
	message = strings.TrimSpace(message)
	if message == "" {
		return errs.Usage("the commit message must not be empty")
	}
	args := []string{"commit", "-m", message}
	if all {
		args = append(args, "-a")
	}
	_, err := c.run(ctx, args...)
	return err
}

// CommitInfo describes the last commit on a path.
type CommitInfo struct {
	Hash    string
	Author  string
	Date    string
	Subject string
}

// LastCommit returns the most recent commit touching path.
func (c *Client) LastCommit(ctx context.Context, path string) (CommitInfo, error) {
	args := []string{"log", "-1", "--format=%H|%an|%ad|%s"}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return CommitInfo{}, err
	}
	fields := strings.SplitN(strings.TrimSpace(out), "|", 4)
	for len(fields) < 4 {
		fields = append(fields, "")
	}
	return CommitInfo{Hash: fields[0], Author: fields[1], Date: fields[2], Subject: fields[3]}, nil
}

// RepoRoot finds the repository root for dir, walking upwards. It returns ""
// when dir is not inside a repository.
func RepoRoot(ctx context.Context, dir string) string {
	client := New(dir)
	if !client.IsRepo(ctx) {
		return ""
	}
	root, err := client.TopLevel(ctx)
	if err != nil {
		return ""
	}
	return filepath.Clean(root)
}
