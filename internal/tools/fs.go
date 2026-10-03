package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/CRISTOP-bot/talon/internal/diff"
	"github.com/CRISTOP-bot/talon/internal/index"
	"github.com/CRISTOP-bot/talon/internal/journal"
	"github.com/CRISTOP-bot/talon/internal/llm"
	"github.com/CRISTOP-bot/talon/internal/perm"
)

// FSDefinitions returns the filesystem tools.
func FSDefinitions() []*Definition {
	return []*Definition{
		readFileDef(),
		writeFileDef(),
		editFileDef(),
		deleteFileDef(),
		listDirectoryDef(),
		searchFilesDef(),
		searchTextDef(),
	}
}

func readFileDef() *Definition {
	return &Definition{
		Name: "read_file",
		Description: "Read a UTF-8 text file from the project. Returns the content with 1-based line numbers. " +
			"Use offset/limit for large files.",
		Parameters: llm.JSONSchema(map[string]any{
			"path":   llm.Prop("string", "Workspace-relative path, e.g. src/main.go"),
			"offset": map[string]any{"type": "integer", "description": "First line to read (1-based)", "minimum": 1},
			"limit":  map[string]any{"type": "integer", "description": "Maximum number of lines to read", "minimum": 1},
		}, "path"),
		Risk:      perm.RiskRead,
		ReadOnly:  true,
		PathArg:   "path",
		Summarize: summarizeWith("path", "Read %s"),
		Handler:   handleReadFile,
	}
}

type readFileArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func handleReadFile(ctx *Context, raw json.RawMessage) (Result, error) {
	var a readFileArgs
	if err := decode("read_file", raw, &a); err != nil {
		return Result{}, err
	}
	abs, err := ctx.resolvePath(a.Path)
	if err != nil {
		return Result{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Result{}, fmt.Errorf("read_file: %w", notFoundOrMissing(a.Path, err))
	}
	if info.IsDir() {
		return Result{}, argError("read_file", "%s is a directory; use list_directory", a.Path)
	}
	if info.Size() > ctx.Limits.MaxReadBytes {
		return Result{}, argError("read_file",
			"%s is %d bytes, larger than the %d byte limit; use search_text or read a range",
			a.Path, info.Size(), ctx.Limits.MaxReadBytes)
	}
	if err := ctx.GuardPath(a.Path); err != nil {
		if ctx.Gates.Audit != nil {
			ctx.Gates.Audit.ToolDenied("read_file", a.Path, "sensitive-file policy")
		}
		return Result{}, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return Result{}, fmt.Errorf("read_file: %w", err)
	}
	body, err := ctx.Sanitize(a.Path, string(data))
	if err != nil {
		return Result{}, err
	}
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	total := len(lines)
	start := a.Offset
	if start <= 0 {
		start = 1
	}
	end := total
	if a.Limit > 0 && start+a.Limit-1 < total {
		end = start + a.Limit - 1
	}
	var b strings.Builder
	if start > total {
		b.WriteString(fmt.Sprintf("The file %s has %d lines; offset %d is past the end.\n", a.Path, total, start))
	} else {
		for i := start; i <= end; i++ {
			fmt.Fprintf(&b, "%6d\t%s\n", i, lines[i-1])
		}
	}
	var header string
	if end < total {
		header = fmt.Sprintf("%s (lines %d-%d of %d)\n", a.Path, start, end, total)
	} else {
		header = fmt.Sprintf("%s (%d lines, %d bytes)\n", a.Path, total, info.Size())
	}
	return Result{
		Content: header + b.String(),
		Display: fmt.Sprintf("read %s (%d lines)", a.Path, end-start+1),
		Metadata: map[string]any{
			"path":  a.Path,
			"lines": total,
			"size":  info.Size(),
		},
	}, nil
}

func writeFileDef() *Definition {
	return &Definition{
		Name: "write_file",
		Description: "Create a file or replace its entire content. Prefer edit_file for small changes: " +
			"write_file overwrites everything. Parent directories are created automatically.",
		Parameters: llm.JSONSchema(map[string]any{
			"path":        llm.Prop("string", "Workspace-relative path of the file to write"),
			"content":     llm.Prop("string", "The full new content of the file"),
			"create_dirs": map[string]any{"type": "boolean", "description": "Create missing parent directories (default true)"},
		}, "path", "content"),
		Risk:      perm.RiskWrite,
		PathArg:   "path",
		Summarize: summarizeWith("path", "Write %s"),
		Handler:   handleWriteFile,
	}
}

type writeFileArgs struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	CreateDirs *bool  `json:"create_dirs"`
}

func handleWriteFile(ctx *Context, raw json.RawMessage) (Result, error) {
	var a writeFileArgs
	if err := decode("write_file", raw, &a); err != nil {
		return Result{}, err
	}
	abs, err := ctx.resolvePath(a.Path)
	if err != nil {
		return Result{}, err
	}
	before, beforeExisted, err := readIfExists(abs)
	if err != nil {
		return Result{}, fmt.Errorf("write_file: %w", err)
	}
	createDirs := true
	if a.CreateDirs != nil {
		createDirs = *a.CreateDirs
	}
	if createDirs {
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return Result{}, argError("write_file", "cannot create the parent directory: %v", err)
		}
	}
	mode := os.FileMode(0o644)
	if beforeExisted {
		if info, serr := os.Stat(abs); serr == nil {
			mode = info.Mode().Perm()
		}
	}
	if err := os.WriteFile(abs, []byte(a.Content), mode); err != nil {
		return Result{}, argError("write_file", "cannot write the file: %v", err)
	}
	record(ctx, a.Path, before, beforeExisted, a.Content, "write_file")
	if ctx.Gates.Audit != nil {
		ctx.Gates.Audit.ToolAllowed("write_file", a.Path)
	}
	fd := diff.FileDiff{Path: a.Path}
	fd.Hunks, fd.Stats = diff.Diff(before, a.Content, ctx.Limits.DefaultContextLines)
	verb := "Created"
	if beforeExisted {
		verb = "Wrote"
	}
	return Result{
		Content: fmt.Sprintf("%s %s (%d bytes, %s).", verb, a.Path, len(a.Content), diff.Preview(fd.Stats)),
		Display: fmt.Sprintf("%s %s %s", verb, a.Path, diff.Preview(fd.Stats)),
		Diff:    &fd,
		Changed: []string{a.Path},
		Metadata: map[string]any{
			"path":    a.Path,
			"created": !beforeExisted,
			"stats":   fd.Stats,
		},
	}, nil
}

func editFileDef() *Definition {
	return &Definition{
		Name: "edit_file",
		Description: "Replace an exact string in a file. old_string must appear exactly once unless " +
			"replace_all is true. Include a few surrounding lines of context to make the match unique.",
		Parameters: llm.JSONSchema(map[string]any{
			"path":        llm.Prop("string", "Workspace-relative path of the file to edit"),
			"old_string":  llm.Prop("string", "The exact text to replace, including indentation"),
			"new_string":  llm.Prop("string", "The replacement text"),
			"replace_all": map[string]any{"type": "boolean", "description": "Replace every occurrence instead of requiring uniqueness"},
		}, "path", "old_string", "new_string"),
		Risk:      perm.RiskWrite,
		PathArg:   "path",
		Summarize: summarizeWith("path", "Edit %s"),
		Handler:   handleEditFile,
	}
}

type editFileArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func handleEditFile(ctx *Context, raw json.RawMessage) (Result, error) {
	var a editFileArgs
	if err := decode("edit_file", raw, &a); err != nil {
		return Result{}, err
	}
	abs, err := ctx.resolvePath(a.Path)
	if err != nil {
		return Result{}, err
	}
	before, existed, err := readIfExists(abs)
	if err != nil {
		return Result{}, fmt.Errorf("edit_file: %w", err)
	}
	if !existed {
		return Result{}, argError("edit_file", "%s does not exist; use write_file to create it", a.Path)
	}
	if a.OldString == "" {
		return Result{}, argError("edit_file", "old_string must not be empty")
	}
	if a.OldString == a.NewString {
		return Result{}, argError("edit_file", "old_string and new_string are identical")
	}
	count := strings.Count(before, a.OldString)
	if count == 0 {
		return Result{}, argError("edit_file",
			"old_string was not found in %s; read the file again and copy the exact text (including indentation)", a.Path)
	}
	if count > 1 && !a.ReplaceAll {
		return Result{}, argError("edit_file",
			"old_string appears %d times in %s; add more surrounding context or set replace_all", count, a.Path)
	}
	var updated string
	if a.ReplaceAll {
		updated = strings.ReplaceAll(before, a.OldString, a.NewString)
	} else {
		idx := strings.Index(before, a.OldString)
		updated = before[:idx] + a.NewString + before[idx+len(a.OldString):]
	}
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return Result{}, argError("edit_file", "cannot write the file: %v", err)
	}
	record(ctx, a.Path, before, true, updated, "edit_file")
	if ctx.Gates.Audit != nil {
		ctx.Gates.Audit.ToolAllowed("edit_file", a.Path)
	}
	fd := diff.FileDiff{Path: a.Path}
	fd.Hunks, fd.Stats = diff.Diff(before, updated, ctx.Limits.DefaultContextLines)
	replaced := count
	if a.ReplaceAll {
		replaced = count
	} else {
		replaced = 1
	}
	return Result{
		Content: fmt.Sprintf("Edited %s (%d replacement%s, %s).",
			a.Path, replaced, plural(replaced), diff.Preview(fd.Stats)),
		Display: fmt.Sprintf("edited %s %s", a.Path, diff.Preview(fd.Stats)),
		Diff:    &fd,
		Changed: []string{a.Path},
		Metadata: map[string]any{
			"path":         a.Path,
			"replacements": replaced,
			"stats":        fd.Stats,
		},
	}, nil
}

func deleteFileDef() *Definition {
	return &Definition{
		Name: "delete_file",
		Description: "Delete a file or an empty directory. This is destructive and always requires " +
			"user confirmation. Directories are only removed when empty.",
		Parameters: llm.JSONSchema(map[string]any{
			"path":      llm.Prop("string", "Workspace-relative path to delete"),
			"recursive": map[string]any{"type": "boolean", "description": "Remove a directory and its contents"},
		}, "path"),
		Risk:      perm.RiskDanger,
		PathArg:   "path",
		Summarize: summarizeWith("path", "Delete %s"),
		Handler:   handleDeleteFile,
	}
}

type deleteFileArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
}

func handleDeleteFile(ctx *Context, raw json.RawMessage) (Result, error) {
	var a deleteFileArgs
	if err := decode("delete_file", raw, &a); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(a.Path) == "" || a.Path == "." || a.Path == "/" {
		return Result{}, argError("delete_file", "refusing to delete %q", a.Path)
	}
	abs, err := ctx.resolvePath(a.Path)
	if err != nil {
		return Result{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Result{}, argError("delete_file", "cannot stat %s: %v", a.Path, err)
	}
	if info.IsDir() {
		if !a.Recursive {
			if err := os.Remove(abs); err != nil {
				return Result{}, argError("delete_file",
					"the directory is not empty (use recursive=true): %v", err)
			}
			record(ctx, a.Path, "", false, "", "delete_file")
			return Result{
				Content: fmt.Sprintf("Removed empty directory %s.", a.Path),
				Display: "removed directory " + a.Path,
				Changed: []string{a.Path},
			}, nil
		}
		if err := os.RemoveAll(abs); err != nil {
			return Result{}, argError("delete_file", "cannot remove %s: %v", a.Path, err)
		}
		record(ctx, a.Path, "", false, "", "delete_file")
		return Result{
			Content: fmt.Sprintf("Deleted directory %s and its contents.", a.Path),
			Display: "deleted " + a.Path,
			Changed: []string{a.Path},
		}, nil
	}
	before, err := os.ReadFile(abs)
	if err != nil {
		return Result{}, argError("delete_file", "cannot read %s before deleting: %v", a.Path, err)
	}
	if err := os.Remove(abs); err != nil {
		return Result{}, argError("delete_file", "cannot delete %s: %v", a.Path, err)
	}
	record(ctx, a.Path, string(before), true, "", "delete_file")
	if ctx.Gates.Audit != nil {
		ctx.Gates.Audit.ToolAllowed("delete_file", a.Path)
	}
	return Result{
		Content: fmt.Sprintf("Deleted %s (%d bytes).", a.Path, len(before)),
		Display: "deleted " + a.Path,
		Changed: []string{a.Path},
	}, nil
}

func listDirectoryDef() *Definition {
	return &Definition{
		Name: "list_directory",
		Description: "List the entries of a directory with type and size. Set recursive=true for a tree " +
			"view; results are limited to avoid flooding the context.",
		Parameters: llm.JSONSchema(map[string]any{
			"path":      llm.Prop("string", "Workspace-relative directory (default: the project root)"),
			"recursive": map[string]any{"type": "boolean", "description": "Walk subdirectories"},
			"limit":     map[string]any{"type": "integer", "description": "Maximum entries to return", "minimum": 1},
		}),
		Risk:      perm.RiskRead,
		ReadOnly:  true,
		PathArg:   "path",
		Summarize: summarizeWith("path", "List %s"),
		Handler:   handleListDirectory,
	}
}

type listDirectoryArgs struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	Limit     int    `json:"limit"`
}

func handleListDirectory(ctx *Context, raw json.RawMessage) (Result, error) {
	var a listDirectoryArgs
	if err := decode("list_directory", raw, &a); err != nil {
		return Result{}, err
	}
	target := a.Path
	if strings.TrimSpace(target) == "" {
		target = "."
	}
	abs, err := ctx.resolvePath(target)
	if err != nil {
		return Result{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Result{}, argError("list_directory", "cannot read %s: %v", target, err)
	}
	if !info.IsDir() {
		return Result{}, argError("list_directory", "%s is a file; use read_file", target)
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 200
	}
	type row struct {
		name string
		kind string
		size int64
	}
	var rows []row
	if a.Recursive {
		rootDepth := strings.Count(abs, string(filepath.Separator))
		_ = filepath.WalkDir(abs, func(path string, d os.DirEntry, werr error) error {
			if werr != nil {
				return nil
			}
			if path == abs {
				return nil
			}
			depth := strings.Count(path, string(filepath.Separator)) - rootDepth
			if depth > 3 {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			fi, ferr := d.Info()
			if ferr != nil {
				return nil
			}
			rel, _ := filepath.Rel(abs, path)
			kind := "file"
			if d.IsDir() {
				kind = "dir "
			}
			rows = append(rows, row{name: filepath.ToSlash(rel), kind: kind, size: fi.Size()})
			return nil
		})
	} else {
		entries, rerr := os.ReadDir(abs)
		if rerr != nil {
			return Result{}, argError("list_directory", "cannot read %s: %v", target, rerr)
		}
		for _, e := range entries {
			fi, ferr := e.Info()
			size := int64(0)
			if ferr == nil {
				size = fi.Size()
			}
			kind := "file"
			if e.IsDir() {
				kind = "dir "
				size = 0
			}
			rows = append(rows, row{name: e.Name(), kind: kind, size: size})
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if (rows[i].kind == "dir ") != (rows[j].kind == "dir ") {
			return rows[i].kind == "dir "
		}
		return rows[i].name < rows[j].name
	})
	truncated := ""
	if len(rows) > limit {
		rows = rows[:limit]
		truncated = fmt.Sprintf("\n… %d more entries not shown", len(rows))
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s:\n", target)
	for _, r := range rows {
		if r.kind == "dir " {
			fmt.Fprintf(&b, "  %s %s/\n", r.kind, r.name)
			continue
		}
		fmt.Fprintf(&b, "  %s %s (%s)\n", r.kind, r.name, humanSize(r.size))
	}
	b.WriteString(truncated)
	return Result{
		Content: b.String(),
		Display: fmt.Sprintf("listed %d entries in %s", len(rows), target),
	}, nil
}

func searchFilesDef() *Definition {
	return &Definition{
		Name:        "search_files",
		Description: "Find files by name or path pattern, e.g. '**/*.go', '*_test.go' or 'parser'. Results are project files, not matches inside them.",
		Parameters: llm.JSONSchema(map[string]any{
			"pattern": llm.Prop("string", "Glob or substring, e.g. **/*.ts or parser"),
			"limit":   map[string]any{"type": "integer", "description": "Maximum results", "minimum": 1},
		}, "pattern"),
		Risk:      perm.RiskRead,
		ReadOnly:  true,
		Summarize: summarizeWith("pattern", "Search files matching %s"),
		Handler:   handleSearchFiles,
	}
}

func handleSearchFiles(ctx *Context, raw json.RawMessage) (Result, error) {
	var a struct {
		Pattern string `json:"pattern"`
		Limit   int    `json:"limit"`
	}
	if err := decode("search_files", raw, &a); err != nil {
		return Result{}, err
	}
	limit := a.Limit
	if limit <= 0 {
		limit = 60
	}
	files := ctx.Index.FindFiles(a.Pattern, limit)
	if len(files) == 0 {
		return Result{
			Content: fmt.Sprintf("No files match %q.", a.Pattern),
			Display: "no files matched " + a.Pattern,
		}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d files matching %q:\n", len(files), a.Pattern)
	for _, f := range files {
		fmt.Fprintf(&b, "  %s\n", f)
	}
	return Result{
		Content: b.String(),
		Display: fmt.Sprintf("found %d files", len(files)),
	}, nil
}

func searchTextDef() *Definition {
	return &Definition{
		Name: "search_text",
		Description: "Search the content of project files. Returns file:line matches. Use regex=true for " +
			"regular expressions; otherwise the query is a plain substring.",
		Parameters: llm.JSONSchema(map[string]any{
			"query":          llm.Prop("string", "Text or regular expression to look for"),
			"regex":          map[string]any{"type": "boolean", "description": "Treat query as a regular expression"},
			"glob":           llm.Prop("string", "Restrict to files matching this glob"),
			"case_sensitive": map[string]any{"type": "boolean", "description": "Match case exactly"},
			"include_tests":  map[string]any{"type": "boolean", "description": "Include test files (default true)"},
			"max_results":    map[string]any{"type": "integer", "description": "Maximum matches", "minimum": 1},
		}, "query"),
		Risk:      perm.RiskRead,
		ReadOnly:  true,
		Summarize: summarizeWith("query", "Search for %s"),
		Handler:   handleSearchText,
	}
}

func handleSearchText(ctx *Context, raw json.RawMessage) (Result, error) {
	var a struct {
		Query         string `json:"query"`
		Regex         bool   `json:"regex"`
		Glob          string `json:"glob"`
		CaseSensitive bool   `json:"case_sensitive"`
		IncludeTests  *bool  `json:"include_tests"`
		MaxResults    int    `json:"max_results"`
	}
	if err := decode("search_text", raw, &a); err != nil {
		return Result{}, err
	}
	includeTests := true
	if a.IncludeTests != nil {
		includeTests = *a.IncludeTests
	}
	matches, err := ctx.Index.Search(ctx.Ctx, indexSearchOptions(a, includeTests))
	if err != nil {
		return Result{}, argError("search_text", "%v", err)
	}
	if len(matches) == 0 {
		return Result{
			Content: fmt.Sprintf("No matches for %q.", a.Query),
			Display: "no matches for " + a.Query,
		}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d matches for %q:\n", len(matches), a.Query)
	for _, m := range matches {
		line, serr := ctx.Sanitize(m.Path, m.Text)
		if serr != nil {
			fmt.Fprintf(&b, "  %s:%d: %s\n", m.Path, m.Line, serr.Error())
			continue
		}
		fmt.Fprintf(&b, "  %s:%d: %s\n", m.Path, m.Line, strings.TrimSpace(line))
	}
	return Result{
		Content:  b.String(),
		Display:  fmt.Sprintf("%d matches", len(matches)),
		Metadata: map[string]any{"matches": len(matches)},
	}, nil
}

// indexSearchOptions maps tool arguments onto index search options.
func indexSearchOptions(a struct {
	Query         string `json:"query"`
	Regex         bool   `json:"regex"`
	Glob          string `json:"glob"`
	CaseSensitive bool   `json:"case_sensitive"`
	IncludeTests  *bool  `json:"include_tests"`
	MaxResults    int    `json:"max_results"`
}, includeTests bool) index.SearchOptions {
	return index.SearchOptions{
		Query:         a.Query,
		Regex:         a.Regex,
		Glob:          a.Glob,
		CaseSensitive: a.CaseSensitive,
		IncludeTests:  includeTests,
		MaxResults:    a.MaxResults,
	}
}

// record appends a change to the undo journal.
func record(ctx *Context, path, before string, beforeExisted bool, after, tool string) {
	if ctx.Journal == nil {
		return
	}
	ctx.Journal.Record(journal.Entry{
		Path:          path,
		Before:        before,
		BeforeExisted: beforeExisted,
		After:         after,
		Tool:          tool,
		Time:          time.Now(),
	})
}

func readIfExists(abs string) (string, bool, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", false, nil
		}
		return "", false, err
	}
	return string(data), true, nil
}

func notFoundOrMissing(path string, err error) error {
	if os.IsNotExist(err) {
		return fmt.Errorf("%s does not exist", path)
	}
	if os.IsPermission(err) {
		return fmt.Errorf("%s is not readable: permission denied", path)
	}
	return err
}

func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// summarizeWith builds a Summarize function using one argument.
func summarizeWith(arg, format string) func(json.RawMessage) string {
	return func(raw json.RawMessage) string {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			return format
		}
		v, _ := m[arg].(string)
		if v == "" {
			return format
		}
		return fmt.Sprintf(format, v)
	}
}
