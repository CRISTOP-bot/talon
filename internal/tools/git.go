package tools

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/talon-cli/talon/internal/git"
	"github.com/talon-cli/talon/internal/llm"
	"github.com/talon-cli/talon/internal/perm"
)

// GitDefinitions returns the git tools.
//
// Talon never pushes: there is no push tool, and git_commit only commits locally.
func GitDefinitions() []*Definition {
	return []*Definition{
		gitStatusDef(),
		gitDiffDef(),
		gitLogDef(),
		gitBranchDef(),
		gitCommitDef(),
		gitCheckoutDef(),
	}
}

func requireGit(ctx *Context) error {
	if ctx.Git == nil {
		return argError("git", "git integration is unavailable")
	}
	if !ctx.Git.IsRepo(ctx.Ctx) {
		return argError("git", "this directory is not a git repository; run `git init` first")
	}
	return nil
}

func gitStatusDef() *Definition {
	return &Definition{
		Name: "git_status",
		Description: "Show the git working tree status: current branch and the modified, staged, " +
			"deleted and untracked files.",
		Parameters: llm.JSONSchema(map[string]any{}),
		Risk:       perm.RiskRead,
		ReadOnly:   true,
		Summarize:  func(json.RawMessage) string { return "Show git status" },
		Handler:    handleGitStatus,
	}
}

func handleGitStatus(ctx *Context, raw json.RawMessage) (Result, error) {
	if err := requireGit(ctx); err != nil {
		return Result{}, err
	}
	branch, _ := ctx.Git.CurrentBranch(ctx.Ctx)
	files, err := ctx.Git.Status(ctx.Ctx)
	if err != nil {
		return Result{}, err
	}
	if len(files) == 0 {
		return Result{
			Content: fmt.Sprintf("Branch %s: the working tree is clean.", branch),
			Display: "git: clean (" + branch + ")",
		}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Branch %s, %d changed file(s):\n", branch, len(files))
	for _, f := range files {
		stage := " "
		if f.Staged {
			stage = "S"
		}
		fmt.Fprintf(&b, "  [%s] %-9s %s\n", stage, f.Label(), f.Path)
	}
	return Result{
		Content: b.String(),
		Display: fmt.Sprintf("git: %d changed files on %s", len(files), branch),
	}, nil
}

func gitDiffDef() *Definition {
	return &Definition{
		Name: "git_diff",
		Description: "Return the unified diff of the working tree. Use staged=true for the staged " +
			"changes, and pass paths to restrict the diff.",
		Parameters: llm.JSONSchema(map[string]any{
			"staged": map[string]any{"type": "boolean", "description": "Diff the index instead of the working tree"},
			"path":   llm.Prop("string", "Restrict the diff to one path"),
			"stat":   map[string]any{"type": "boolean", "description": "Return a per-file summary instead of the patch"},
		}),
		Risk:      perm.RiskRead,
		ReadOnly:  true,
		PathArg:   "path",
		Summarize: summarizeGitDiff,
		Handler:   handleGitDiff,
	}
}

func summarizeGitDiff(raw json.RawMessage) string {
	var a struct {
		Staged bool   `json:"staged"`
		Path   string `json:"path"`
		Stat   bool   `json:"stat"`
	}
	_ = json.Unmarshal(raw, &a)
	scope := "working tree"
	if a.Staged {
		scope = "index"
	}
	if a.Path != "" {
		return fmt.Sprintf("Show the %s diff for %s", scope, a.Path)
	}
	return "Show the " + scope + " diff"
}

func handleGitDiff(ctx *Context, raw json.RawMessage) (Result, error) {
	if err := requireGit(ctx); err != nil {
		return Result{}, err
	}
	var a struct {
		Staged bool   `json:"staged"`
		Path   string `json:"path"`
		Stat   bool   `json:"stat"`
	}
	if err := decode("git_diff", raw, &a); err != nil {
		return Result{}, err
	}
	var out string
	var err error
	if a.Stat {
		out, err = ctx.Git.DiffStat(ctx.Ctx, a.Staged)
	} else if a.Path != "" {
		abs, rerr := ctx.resolvePath(a.Path)
		if rerr != nil {
			return Result{}, rerr
		}
		out, err = ctx.Git.Diff(ctx.Ctx, a.Staged, ctx.Rel(abs))
	} else {
		out, err = ctx.Git.Diff(ctx.Ctx, a.Staged)
	}
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(out) == "" {
		scope := "working tree"
		if a.Staged {
			scope = "index"
		}
		return Result{
			Content: "There are no changes in the " + scope + ".",
			Display: "git diff: empty",
		}, nil
	}
	return Result{
		Content: out,
		Display: fmt.Sprintf("git diff (%d bytes)", len(out)),
	}, nil
}

func gitLogDef() *Definition {
	return &Definition{
		Name:        "git_log",
		Description: "Show recent commits. Optionally filter by path, author or date range.",
		Parameters: llm.JSONSchema(map[string]any{
			"limit":  map[string]any{"type": "integer", "description": "How many commits to show", "minimum": 1},
			"path":   llm.Prop("string", "Only commits touching this path"),
			"author": llm.Prop("string", "Filter by author substring"),
			"since":  llm.Prop("string", "Only commits after this date, e.g. \"2 weeks ago\""),
		}),
		Risk:      perm.RiskRead,
		ReadOnly:  true,
		PathArg:   "path",
		Summarize: summarizeWith("path", "Show git history for %s"),
		Handler:   handleGitLog,
	}
}

func handleGitLog(ctx *Context, raw json.RawMessage) (Result, error) {
	if err := requireGit(ctx); err != nil {
		return Result{}, err
	}
	var a struct {
		Limit  int    `json:"limit"`
		Path   string `json:"path"`
		Author string `json:"author"`
		Since  string `json:"since"`
	}
	if err := decode("git_log", raw, &a); err != nil {
		return Result{}, err
	}
	commits, err := ctx.Git.Log(ctx.Ctx, git.LogOptions{
		Limit:  a.Limit,
		Path:   a.Path,
		Author: a.Author,
		Since:  a.Since,
	})
	if err != nil {
		return Result{}, err
	}
	if len(commits) == 0 {
		return Result{Content: "No commits matched.", Display: "git log: empty"}, nil
	}
	var b strings.Builder
	for _, c := range commits {
		fmt.Fprintf(&b, "%s  %s  %s\n", c.Short(), c.Date, c.Subject)
		if c.Author != "" {
			fmt.Fprintf(&b, "        %s\n", c.Author)
		}
	}
	return Result{
		Content: b.String(),
		Display: fmt.Sprintf("git log: %d commits", len(commits)),
	}, nil
}

func gitBranchDef() *Definition {
	return &Definition{
		Name:        "git_branch",
		Description: "List the local branches and mark the current one.",
		Parameters:  llm.JSONSchema(map[string]any{}),
		Risk:        perm.RiskRead,
		ReadOnly:    true,
		Summarize:   func(json.RawMessage) string { return "List git branches" },
		Handler:     handleGitBranch,
	}
}

func handleGitBranch(ctx *Context, raw json.RawMessage) (Result, error) {
	if err := requireGit(ctx); err != nil {
		return Result{}, err
	}
	branches, current, err := ctx.Git.Branches(ctx.Ctx)
	if err != nil {
		return Result{}, err
	}
	var b strings.Builder
	for _, br := range branches {
		marker := "  "
		if br == current {
			marker = "* "
		}
		fmt.Fprintf(&b, "%s%s\n", marker, br)
	}
	return Result{Content: b.String(), Display: fmt.Sprintf("git: %d branches", len(branches))}, nil
}

func gitCommitDef() *Definition {
	return &Definition{
		Name: "git_commit",
		Description: "Create a local commit. Stage the given paths first (paths=[\".\"] stages everything). " +
			"This never pushes. Commit messages must describe the change in the imperative mood.",
		Parameters: llm.JSONSchema(map[string]any{
			"message": llm.Prop("string", "The commit message subject"),
			"paths": map[string]any{
				"type":        "array",
				"description": "Paths to stage; use [\".\"] for all changes",
				"items":       map[string]any{"type": "string"},
			},
		}, "message"),
		Risk:      perm.RiskWrite,
		Summarize: summarizeCommit,
		Handler:   handleGitCommit,
	}
}

func summarizeCommit(raw json.RawMessage) string {
	var a struct {
		Message string   `json:"message"`
		Paths   []string `json:"paths"`
	}
	if err := json.Unmarshal(raw, &a); err != nil || a.Message == "" {
		return "Create a git commit"
	}
	if len(a.Paths) > 0 {
		return fmt.Sprintf("Commit %q (staging %s)", a.Message, strings.Join(a.Paths, ", "))
	}
	return fmt.Sprintf("Commit %q", a.Message)
}

func handleGitCommit(ctx *Context, raw json.RawMessage) (Result, error) {
	if err := requireGit(ctx); err != nil {
		return Result{}, err
	}
	var a struct {
		Message string   `json:"message"`
		Paths   []string `json:"paths"`
	}
	if err := decode("git_commit", raw, &a); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(a.Message) == "" {
		return Result{}, argError("git_commit", "the commit message must not be empty")
	}
	paths := a.Paths
	if len(paths) == 0 {
		paths = []string{"."}
	}
	for _, p := range paths {
		if _, err := ctx.resolvePath(p); err != nil {
			return Result{}, argError("git_commit", "invalid path %q: %v", p, err)
		}
	}
	if err := ctx.Git.Add(ctx.Ctx, paths...); err != nil {
		return Result{}, err
	}
	if err := ctx.Git.Commit(ctx.Ctx, a.Message, false); err != nil {
		return Result{}, err
	}
	info, _ := ctx.Git.LastCommit(ctx.Ctx, "")
	hash := info.Hash
	if len(hash) > 8 {
		hash = hash[:8]
	}
	return Result{
		Content: fmt.Sprintf("Created commit %s: %s\n\n%s", hash, info.Subject,
			"Note: the commit is local; use `git push` yourself if the remote should receive it."),
		Display:  "committed " + hash + ": " + info.Subject,
		Metadata: map[string]any{"commit": hash},
	}, nil
}

func gitCheckoutDef() *Definition {
	return &Definition{
		Name: "git_checkout",
		Description: "Switch to another branch, or create a new one. Working tree changes are " +
			"preserved; use git_reset if a hard reset is really needed (that must be run manually).",
		Parameters: llm.JSONSchema(map[string]any{
			"branch": llm.Prop("string", "Branch to check out"),
			"create": map[string]any{"type": "boolean", "description": "Create the branch before switching"},
		}, "branch"),
		Risk:      perm.RiskExec,
		Summarize: summarizeWith("branch", "Check out branch %s"),
		Handler:   handleGitCheckout,
	}
}

func handleGitCheckout(ctx *Context, raw json.RawMessage) (Result, error) {
	if err := requireGit(ctx); err != nil {
		return Result{}, err
	}
	var a struct {
		Branch string `json:"branch"`
		Create bool   `json:"create"`
	}
	if err := decode("git_checkout", raw, &a); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(a.Branch) == "" {
		return Result{}, argError("git_checkout", "the branch name must not be empty")
	}
	previous, _ := ctx.Git.CurrentBranch(ctx.Ctx)
	if err := ctx.Git.Checkout(ctx.Ctx, a.Branch, a.Create); err != nil {
		return Result{}, err
	}
	current, _ := ctx.Git.CurrentBranch(ctx.Ctx)
	return Result{
		Content: fmt.Sprintf("Switched from %s to %s.", previous, current),
		Display: "checked out " + current,
	}, nil
}
