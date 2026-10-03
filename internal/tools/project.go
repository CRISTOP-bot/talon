package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/talon-cli/talon/internal/diff"
	"github.com/talon-cli/talon/internal/llm"
	"github.com/talon-cli/talon/internal/perm"
	"github.com/talon-cli/talon/internal/project"
)

// ProjectDefinitions returns the project-level tools.
func ProjectDefinitions() []*Definition {
	return []*Definition{
		inspectProjectDef(),
		listSymbolsDef(),
		applyPatchDef(),
	}
}

func inspectProjectDef() *Definition {
	return &Definition{
		Name: "inspect_project",
		Description: "Inspect the project: detected language, frameworks, build system, package manager, " +
			"test layout, git state and the files that are worth reading first. Call this once at the " +
			"start of a task when the project structure is unknown.",
		Parameters: llm.JSONSchema(map[string]any{
			"include_tree": map[string]any{"type": "boolean", "description": "Append the top-level file tree"},
			"depth":        map[string]any{"type": "integer", "description": "Tree depth when include_tree is set", "minimum": 1},
		}),
		Risk:      perm.RiskRead,
		ReadOnly:  true,
		Summarize: func(json.RawMessage) string { return "Inspect the project structure" },
		Handler:   handleInspectProject,
	}
}

func handleInspectProject(ctx *Context, raw json.RawMessage) (Result, error) {
	var a struct {
		IncludeTree bool `json:"include_tree"`
		Depth       int  `json:"depth"`
	}
	if err := decode("inspect_project", raw, &a); err != nil {
		return Result{}, err
	}
	p := ctx.Project
	if p == nil {
		return Result{}, argError("inspect_project", "project information is not available")
	}
	var b strings.Builder
	b.WriteString(p.Describe())

	stats := ctx.Index.Stats()
	fmt.Fprintf(&b, "\nIndexed: %d files, %s\n", stats.Files, languageBreakdown(stats.Languages))
	if stats.Tests > 0 {
		fmt.Fprintf(&b, "Test files: %d\n", stats.Tests)
	}

	if a.IncludeTree {
		depth := a.Depth
		if depth <= 0 {
			depth = 2
		}
		b.WriteString("\nTop-level layout:\n")
		b.WriteString(treeOf(ctx.Workspace, depth))
	}

	if len(importantFiles(ctx)) > 0 {
		b.WriteString("\nFiles worth reading first:\n")
		for _, f := range importantFiles(ctx) {
			b.WriteString("  " + f + "\n")
		}
	}
	return Result{
		Content: b.String(),
		Display: fmt.Sprintf("inspected project (%s, %d files)", orUnknown(p.Primary), stats.Files),
	}, nil
}

// importantFiles lists the files that give the fastest useful overview.
func importantFiles(ctx *Context) []string {
	var out []string
	if ctx.Project == nil {
		return out
	}
	if ctx.Project.ReadmePath != "" {
		out = append(out, ctx.Project.ReadmePath)
	}
	for _, m := range ctx.Project.Manifests {
		out = append(out, m)
	}
	out = append(out, ctx.Project.EntryPoints...)
	for _, t := range ctx.Project.TestDirs {
		if files := ctx.Index.FindFiles(filepath.ToSlash(filepath.Join(t, "*")), 3); len(files) > 0 {
			out = append(out, files[0])
		}
	}
	out = dedupe(out)
	if len(out) > 12 {
		out = out[:12]
	}
	return out
}

func treeOf(root string, depth int) string {
	var lines []string
	var walk func(dir string, prefix string, level int)
	walk = func(dir string, prefix string, level int) {
		if level > depth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		var dirs, files []os.DirEntry
		for _, e := range entries {
			name := e.Name()
			if strings.HasPrefix(name, ".") || project.DefaultIgnoredDirs[name] {
				continue
			}
			if e.IsDir() {
				dirs = append(dirs, e)
			} else {
				files = append(files, e)
			}
		}
		for _, e := range dirs {
			lines = append(lines, prefix+"  "+e.Name()+"/")
			walk(filepath.Join(dir, e.Name()), prefix+"  ", level+1)
		}
		for _, e := range files {
			lines = append(lines, prefix+"  "+e.Name())
		}
	}
	walk(root, "", 1)
	sort.Strings(lines)
	if len(lines) > 200 {
		lines = lines[:200]
		lines = append(lines, "  … truncated")
	}
	return strings.Join(lines, "\n")
}

func languageBreakdown(counts map[string]int) string {
	if len(counts) == 0 {
		return "no languages detected"
	}
	type kv struct {
		k string
		v int
	}
	var pairs []kv
	for k, v := range counts {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].v > pairs[j].v })
	var parts []string
	for i, p := range pairs {
		if i >= 5 {
			break
		}
		parts = append(parts, fmt.Sprintf("%s (%d)", p.k, p.v))
	}
	return strings.Join(parts, ", ")
}

func listSymbolsDef() *Definition {
	return &Definition{
		Name: "list_symbols",
		Description: "List the top-level symbols (functions, types, classes) declared in a file. " +
			"Cheaper than reading a whole file when you only need its shape.",
		Parameters: llm.JSONSchema(map[string]any{
			"path": llm.Prop("string", "Workspace-relative file path"),
		}, "path"),
		Risk:      perm.RiskRead,
		ReadOnly:  true,
		PathArg:   "path",
		Summarize: summarizeWith("path", "List symbols in %s"),
		Handler:   handleListSymbols,
	}
}

func handleListSymbols(ctx *Context, raw json.RawMessage) (Result, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := decode("list_symbols", raw, &a); err != nil {
		return Result{}, err
	}
	for _, e := range ctx.Index.Entries() {
		if e.Path != a.Path {
			continue
		}
		if len(e.Symbols) == 0 {
			return Result{
				Content: fmt.Sprintf("%s declares no recognizable symbols (language: %s).", e.Path, orUnknown(e.Language)),
				Display: "no symbols in " + a.Path,
			}, nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s (%s, %s, %d symbols):\n", e.Path, orUnknown(e.Language), humanSize(e.Size), len(e.Symbols))
		for _, s := range e.Symbols {
			fmt.Fprintf(&b, "  %s\n", s)
		}
		return Result{Content: b.String(), Display: fmt.Sprintf("%d symbols in %s", len(e.Symbols), a.Path)}, nil
	}
	return Result{}, argError("list_symbols", "%s is not part of the project index", a.Path)
}

func applyPatchDef() *Definition {
	return &Definition{
		Name: "apply_patch",
		Description: "Apply a unified diff to one file. Use edit_file for small changes and write_file " +
			"for new files; apply_patch is for large, reviewable diffs. The patch must apply cleanly to " +
			"the current content, otherwise nothing is written.",
		Parameters: llm.JSONSchema(map[string]any{
			"path":  llm.Prop("string", "Workspace-relative file path"),
			"patch": llm.Prop("string", "The unified diff to apply, starting with @@ hunk headers"),
		}, "path", "patch"),
		Risk:      perm.RiskWrite,
		PathArg:   "path",
		Summarize: summarizeWith("path", "Apply a patch to %s"),
		Handler:   handleApplyPatch,
	}
}

func handleApplyPatch(ctx *Context, raw json.RawMessage) (Result, error) {
	var a struct {
		Path  string `json:"path"`
		Patch string `json:"patch"`
	}
	if err := decode("apply_patch", raw, &a); err != nil {
		return Result{}, err
	}
	abs, err := ctx.resolvePath(a.Path)
	if err != nil {
		return Result{}, err
	}
	before, existed, err := readIfExists(abs)
	if err != nil {
		return Result{}, fmt.Errorf("apply_patch: %w", err)
	}
	updated, err := applyUnifiedPatch(before, a.Patch)
	if err != nil {
		return Result{}, argError("apply_patch", "%v", err)
	}
	if !existed {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return Result{}, argError("apply_patch", "cannot create the parent directory: %v", err)
		}
	}
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return Result{}, argError("apply_patch", "cannot write the file: %v", err)
	}
	record(ctx, a.Path, before, existed, updated, "apply_patch")
	fd := diff.FileDiff{Path: a.Path}
	fd.Hunks, fd.Stats = diff.Diff(before, updated, ctx.Limits.DefaultContextLines)
	return Result{
		Content: fmt.Sprintf("Applied the patch to %s (%s).", a.Path, diff.Preview(fd.Stats)),
		Display: fmt.Sprintf("patched %s %s", a.Path, diff.Preview(fd.Stats)),
		Diff:    &fd,
		Changed: []string{a.Path},
	}, nil
}

// applyUnifiedPatch applies a unified diff produced by `git diff` or by the
// agent. It verifies context lines before writing anything.
func applyUnifiedPatch(original, patch string) (string, error) {
	lines := strings.Split(strings.ReplaceAll(original, "\r\n", "\n"), "\n")
	trailingNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}

	patchLines := strings.Split(strings.ReplaceAll(patch, "\r\n", "\n"), "\n")
	var out []string
	pos := 0 // index into lines
	i := 0
	for i < len(patchLines) {
		line := patchLines[i]
		if !strings.HasPrefix(line, "@@") {
			i++
			continue
		}
		// The hunk header gives the expected position; trust it but verify.
		hunkStart, err := parseHunkStart(line)
		if err != nil {
			return "", err
		}
		if hunkStart-1 < pos {
			return "", fmt.Errorf("hunk header @@ -%d overlaps the previous hunk", hunkStart)
		}
		// Copy unchanged lines up to the hunk.
		if hunkStart-1 > len(lines) {
			return "", fmt.Errorf("hunk starts at line %d but the file has %d lines", hunkStart, len(lines))
		}
		out = append(out, lines[pos:hunkStart-1]...)
		pos = hunkStart - 1
		i++
		for i < len(patchLines) {
			pl := patchLines[i]
			if strings.HasPrefix(pl, "@@") || strings.HasPrefix(pl, "diff --git") ||
				strings.HasPrefix(pl, "--- ") || strings.HasPrefix(pl, "+++ ") {
				break
			}
			switch {
			case strings.HasPrefix(pl, " "):
				if pos >= len(lines) || lines[pos] != pl[1:] {
					return "", fmt.Errorf("context mismatch at line %d: expected %q, found %q",
						pos+1, pl[1:], safeIndex(lines, pos))
				}
				out = append(out, lines[pos])
				pos++
			case strings.HasPrefix(pl, "-"):
				if pos >= len(lines) || lines[pos] != pl[1:] {
					return "", fmt.Errorf("removal mismatch at line %d: expected %q, found %q",
						pos+1, pl[1:], safeIndex(lines, pos))
				}
				pos++
			case strings.HasPrefix(pl, "+"):
				out = append(out, pl[1:])
			case strings.HasPrefix(pl, "\\"):
				// "\ No newline at end of file"
			case strings.TrimSpace(pl) == "":
				// blank separator inside the patch
			default:
				return "", fmt.Errorf("unexpected patch line %q", pl)
			}
			i++
		}
	}
	out = append(out, lines[pos:]...)
	result := strings.Join(out, "\n")
	if trailingNewline || !strings.HasSuffix(original, "\n") && len(out) > 0 {
		result += "\n"
	}
	return result, nil
}

func parseHunkStart(header string) (int, error) {
	rest := strings.TrimPrefix(header, "@@")
	if i := strings.Index(rest, " @@"); i >= 0 {
		rest = rest[:i]
	}
	fields := strings.Fields(strings.TrimSpace(rest))
	if len(fields) == 0 {
		return 0, fmt.Errorf("malformed hunk header %q", header)
	}
	start := strings.TrimPrefix(fields[0], "-")
	if start == "0" {
		return 1, nil
	}
	var n int
	if _, err := fmt.Sscanf(start, "%d", &n); err != nil || n < 1 {
		return 0, fmt.Errorf("malformed hunk header %q", header)
	}
	return n, nil
}

func safeIndex(lines []string, i int) string {
	if i < len(lines) {
		return lines[i]
	}
	return "<end of file>"
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// All returns every built-in tool, labelled as such.
func All() []*Definition {
	var out []*Definition
	out = append(out, FSDefinitions()...)
	out = append(out, ShellDefinitions()...)
	out = append(out, GitDefinitions()...)
	out = append(out, ProjectDefinitions()...)
	for _, d := range out {
		d.Source = SourceBuiltin
	}
	return out
}
