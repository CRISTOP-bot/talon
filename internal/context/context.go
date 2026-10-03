// Package context builds what the model is told: the system prompt and the
// project snapshot injected at the start of a session.
//
// The goal is to send enough context to be useful and little enough to leave
// room for the conversation. Talon never dumps a whole repository into the
// prompt: it sends a map of the project plus the files the model asks for.
package context

import (
	"fmt"
	"sort"
	"strings"

	"github.com/talon-cli/talon/internal/config"
	"github.com/talon-cli/talon/internal/index"
	"github.com/talon-cli/talon/internal/memory"
	"github.com/talon-cli/talon/internal/project"
	"github.com/talon-cli/talon/internal/tools"
)

// Builder assembles the system prompt.
type Builder struct {
	Config    *config.Config
	Project   *project.Info
	Index     *index.Index
	Memory    *memory.Store
	Workspace string
	// GitSummary is a short description of the repository state.
	GitSummary string
	// Extra is appended verbatim (used by `--append-system`).
	Extra string
}

// BasePrompt is Talon's standing behaviour description. It is kept short and
// concrete because it is paid for on every request.
const BasePrompt = `You are Talon, an autonomous coding agent working inside a real software project.

How you work:
- Understand before acting. Read the files you need with the provided tools; never guess contents.
- Work in small, verifiable steps: change, then verify with the project's own build or tests.
- Prefer editing existing files over creating new ones, and prefer targeted edits over rewriting whole files.
- Use the tools for everything. You have no other access to the filesystem or the shell.
- When a command fails, read the error output and fix the cause rather than repeating the command.
- Never invent APIs, flags or file contents. If you are unsure, look it up in the repository.
- Keep the user informed: explain what you changed and why in plain language.
- Stop and report when a task is done. Do not start unrelated work.

Style:
- Match the surrounding code: naming, comment density, error handling and structure.
- No placeholder code, no "TODO left for later", no commented-out blocks.
- Keep changes minimal but complete; if something is broken, say so.

Safety:
- Destructive or expensive operations require user confirmation. Ask for it by attempting the tool call; do not try to work around it.
- Never commit secrets, tokens or credentials into the repository.
- Never run git push, force operations, or anything that rewrites history.`

// System renders the full system prompt.
func (b *Builder) System() string {
	var parts []string
	parts = append(parts, BasePrompt)
	if b.Project != nil {
		parts = append(parts, "PROJECT\n"+strings.TrimRight(b.Project.Describe(), "\n"))
	}
	if s := b.indexSummary(); s != "" {
		parts = append(parts, "INDEX\n"+s)
	}
	if b.GitSummary != "" {
		parts = append(parts, "GIT\n"+b.GitSummary)
	}
	if memBlock := b.memoryBlock(); memBlock != "" {
		parts = append(parts, "REMEMBERED CONTEXT\n"+memBlock)
	}
	if b.Extra != "" {
		parts = append(parts, strings.TrimSpace(b.Extra))
	}
	if b.Config != nil {
		parts = append(parts, fmt.Sprintf("PERMISSIONS\nLevel: %s. Operations outside it are refused; "+
			"explain to the user what you would need instead.", b.Config.Permissions.Level))
	}
	return strings.Join(parts, "\n\n")
}

// indexSummary renders a compact map of the codebase: languages, sizes and the
// most relevant directories.
func (b *Builder) indexSummary() string {
	if b.Index == nil {
		return ""
	}
	stats := b.Index.Stats()
	if stats.Files == 0 {
		return "The project index is empty."
	}
	var s strings.Builder
	fmt.Fprintf(&s, "%d files, %d lines.\n", stats.Files, stats.Bytes/40)
	if len(stats.Languages) > 0 {
		type kv struct {
			name  string
			count int
		}
		var pairs []kv
		for name, count := range stats.Languages {
			pairs = append(pairs, kv{name, count})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].count > pairs[j].count })
		var parts []string
		for i, p := range pairs {
			if i >= 6 {
				break
			}
			parts = append(parts, fmt.Sprintf("%s (%d)", p.name, p.count))
		}
		fmt.Fprintf(&s, "Languages: %s\n", strings.Join(parts, ", "))
	}
	if stats.Tests > 0 {
		fmt.Fprintf(&s, "Test files: %d\n", stats.Tests)
	}
	if dirs := b.hotDirs(8); len(dirs) > 0 {
		fmt.Fprintf(&s, "Directories: %s\n", strings.Join(dirs, ", "))
	}
	s.WriteString("Use search_files, search_text and list_symbols to locate things; " +
		"read_file to inspect them.")
	return s.String()
}

// hotDirs lists the directories holding the most source files.
func (b *Builder) hotDirs(limit int) []string {
	counts := map[string]int{}
	for _, e := range b.Index.Entries() {
		if !strings.Contains(e.Path, "/") {
			continue
		}
		dir := e.Path[:strings.Index(e.Path, "/")]
		counts[dir]++
	}
	type kv struct {
		name  string
		count int
	}
	var pairs []kv
	for k, v := range counts {
		pairs = append(pairs, kv{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].count > pairs[j].count })
	var out []string
	for i, p := range pairs {
		if i >= limit {
			break
		}
		out = append(out, fmt.Sprintf("%s/ (%d files)", p.name, p.count))
	}
	return out
}

// memoryBlock renders the remembered notes and any project memory files.
func (b *Builder) memoryBlock() string {
	var parts []string
	if b.Memory != nil && b.Memory.Len() > 0 {
		parts = append(parts, "Notes from previous sessions:\n"+strings.TrimRight(b.Memory.Render(40), "\n"))
	}
	if content, files := memory.ReadProjectMemory(b.Workspace); content != "" {
		parts = append(parts, "Committed with the repository:\n"+content)
		_ = files
	}
	return strings.Join(parts, "\n\n")
}

// ToolGuide documents the tools in the system prompt. It stays short: the
// schemas already carry the argument details.
const ToolGuide = `

TOOLS
Use these instead of shell commands whenever they exist: file reading and editing
go through read_file, write_file, edit_file, list_directory, search_files,
search_text and list_symbols; git inspection goes through git_status, git_diff,
git_log and git_branch; commits go through git_commit. Use run_command,
run_tests and build_project for everything else.`

// WithToolGuide appends the tool guide to a prompt.
func WithToolGuide(prompt string) string {
	return strings.TrimRight(prompt, "\n") + ToolGuide
}

// Suggest ranks the tools that fit a request. It backs `/tools suggest`, which
// teaches users the vocabulary Talon understands.
func Suggest(input string, all []*tools.Definition, limit int) []*tools.Definition {
	lower := strings.ToLower(input)
	type scored struct {
		def   *tools.Definition
		score int
	}
	ranked := make([]scored, 0, len(all))
	for _, d := range all {
		score := 0
		if strings.Contains(lower, strings.ToLower(d.Name)) {
			score += 5
		}
		// The tool's own verb ("commit", "build", "edit") appearing as a whole
		// word is the strongest hint a user can give.
		if verb := toolVerb(d.Name); verb != "" && mentionsVerb(lower, verb) {
			score += 5
		}
		for _, kw := range keywordsFor(d.Name) {
			if !strings.Contains(lower, kw) {
				continue
			}
			// Longer keywords are more specific, and a whole-word match is a
			// stronger signal than an incidental substring.
			score += 1 + len(kw)/8
			if containsWord(lower, kw) {
				score += 2
			}
		}
		if score == 0 {
			continue
		}
		ranked = append(ranked, scored{def: d, score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	out := make([]*tools.Definition, 0, len(ranked))
	for _, r := range ranked {
		out = append(out, r.def)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// keywordsFor maps a tool to the words a user is likely to use when they mean
// that tool.
func keywordsFor(name string) []string {
	switch name {
	case "read_file":
		return []string{"read", "open", "cat", "contents", "show me the file", "load", "inspect file", "review the file"}
	case "write_file":
		return []string{"create", "new file", "write", "scaffold", "generate", "add a file", "start a file"}
	case "edit_file":
		return []string{"change", "replace", "fix", "modify", "rename", "update", "refactor", "edit", "correct"}
	case "apply_patch":
		return []string{"patch", "apply a diff", "apply changes"}
	case "delete_file":
		return []string{"delete", "remove", "drop it", "clean up", "get rid of"}
	case "list_directory":
		return []string{"list", "directory", "folder", "tree", "ls ", "what files"}
	case "search_files":
		return []string{"find files", "find the file", "locate a file", "glob", "filename", "which file", "list files"}
	case "search_text":
		return []string{"grep", "search", "occurrences", "where is", "find", "lookup", "references", "defined"}
	case "list_symbols":
		return []string{"symbols", "declarations", "functions", "types", "what does this file define"}
	case "inspect_project":
		return []string{"project", "overview", "structure", "what is this", "architecture", "analyse", "analyze", "stack"}
	case "run_command":
		return []string{"command", "shell", "run a command", "run the command", "execute", "install", "generate"}
	case "run_tests":
		return []string{"test", "tests", "spec", "failing", "suite", "green"}
	case "build_project":
		return []string{"build", "compile", "type check", "make it", "link"}
	case "git_status":
		return []string{"status", "working tree", "what changed"}
	case "git_diff":
		return []string{"diff", "changes", "review changes", "what did you change"}
	case "git_log":
		return []string{"history", "log", "commits", "who changed", "previous work"}
	case "git_branch":
		return []string{"branch", "branches"}
	case "git_commit":
		return []string{"commit", "checkpoint", "save the work", "record the change"}
	case "git_checkout":
		return []string{"checkout", "switch branch", "change branch"}
	}
	return nil
}

// mentionsVerb reports whether text mentions a verb, tolerating a plural.
func mentionsVerb(text, verb string) bool {
	return containsWord(text, verb) || containsWord(text, verb+"s")
}

// toolVerb returns the action a tool performs, if its name implies one.
func toolVerb(name string) string {
	switch name {
	case "read_file":
		return "read"
	case "write_file":
		return "write"
	case "edit_file":
		return "edit"
	case "delete_file":
		return "delete"
	case "search_text":
		return "search"
	case "search_files":
		// Deliberately no verb: "find the parser" means search content, not
		// find files, and the keywords below disambiguate.
		return ""
	case "list_directory":
		return "list"
	case "run_command":
		// "run the tests" is a test request, not a shell request.
		return ""
	case "run_tests":
		return "test"
	case "build_project":
		return "build"
	case "git_commit":
		return "commit"
	case "git_status":
		return "status"
	case "git_diff":
		return "diff"
	case "git_log":
		return "log"
	case "git_branch":
		return "branch"
	case "git_checkout":
		return "checkout"
	}
	return ""
}

// containsWord reports whether needle appears in text delimited by
// non-alphanumeric characters.
func containsWord(text, needle string) bool {
	trimmed := strings.TrimSpace(needle)
	if trimmed == "" {
		return false
	}
	for i := 0; i+len(trimmed) <= len(text); i++ {
		if text[i:i+len(trimmed)] != trimmed {
			continue
		}
		if i > 0 && isWordByte(text[i-1]) {
			continue
		}
		if i+len(trimmed) < len(text) && isWordByte(text[i+len(trimmed)]) {
			continue
		}
		return true
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func containsAnyWord(text string, words ...string) bool {
	for _, w := range words {
		if strings.Contains(text, w) {
			return true
		}
	}
	return false
}
