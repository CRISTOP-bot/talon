package tools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CRISTOP-bot/talon/internal/git"
	"github.com/CRISTOP-bot/talon/internal/index"
	"github.com/CRISTOP-bot/talon/internal/journal"
	"github.com/CRISTOP-bot/talon/internal/llm"
	"github.com/CRISTOP-bot/talon/internal/logger"
	"github.com/CRISTOP-bot/talon/internal/perm"
	"github.com/CRISTOP-bot/talon/internal/project"
	"github.com/CRISTOP-bot/talon/internal/shell"
)

// newTestContext builds a Context rooted at a temporary directory containing a
// small Go project.
func newTestContext(t *testing.T) (*Context, string) {
	t.Helper()
	dir := t.TempDir()
	write := func(rel, body string) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module example.com/demo\n\ngo 1.22\n")
	write("main.go", "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n")
	write("parser.go", "package main\n\nfunc parse(s string) int {\n\treturn len(s)\n}\n")
	write("parser_test.go", "package main\n\nfunc TestParse(t *testing.T) {}\n")
	write("README.md", "# Demo\n")

	ctx := &Context{
		Ctx:       context.Background(),
		Workspace: dir,
		Limits:    DefaultLimits(),
		Policy:    perm.New(perm.Config{Level: perm.FullAccess, Workspace: dir}),
		Shell:     shell.NewRunner(),
		Git:       git.New(dir),
		Index:     index.New(dir),
		Journal:   journal.New(""),
		Log:       logger.Discard,
	}
	if err := ctx.Index.Build(context.Background()); err != nil {
		t.Fatalf("index build: %v", err)
	}
	ctx.Project = project.Detect(context.Background(), dir)
	return ctx, dir
}

func run(t *testing.T, ctx *Context, name string, args map[string]any) Result {
	t.Helper()
	all, ok := newRegistryWithBuiltins().Get(name)
	if !ok {
		t.Fatalf("tool %s is not registered", name)
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := all.Run(ctx, raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return res
}

func newRegistryWithBuiltins() *Registry {
	r := NewRegistry()
	r.Register(All()...)
	return r
}

func runErr(t *testing.T, ctx *Context, name string, args map[string]any) error {
	t.Helper()
	all, ok := newRegistryWithBuiltins().Get(name)
	if !ok {
		t.Fatalf("tool %s is not registered", name)
	}
	raw, _ := json.Marshal(args)
	_, err := all.Run(ctx, raw)
	return err
}

func TestRegistryHasAllBuiltinTools(t *testing.T) {
	r := newRegistryWithBuiltins()
	want := []string{
		"read_file", "write_file", "edit_file", "delete_file", "list_directory",
		"search_files", "search_text", "run_command", "run_tests", "build_project",
		"git_status", "git_diff", "git_log", "git_branch", "git_commit", "git_checkout",
		"inspect_project", "list_symbols", "apply_patch",
	}
	for _, name := range want {
		if _, ok := r.Get(name); !ok {
			t.Errorf("tool %q is missing", name)
		}
	}
	if r.Len() != len(want) {
		t.Errorf("registry has %d tools, want %d", r.Len(), len(want))
	}
	for _, d := range r.All() {
		if d.Description == "" {
			t.Errorf("tool %q has no description", d.Name)
		}
		if d.Parameters["type"] != "object" {
			t.Errorf("tool %q has an invalid schema", d.Name)
		}
	}
}

func TestNoPushToolExists(t *testing.T) {
	r := newRegistryWithBuiltins()
	for _, d := range r.All() {
		if strings.Contains(strings.ToLower(d.Name), "push") {
			t.Errorf("tool %q would allow pushing; Talon must never push", d.Name)
		}
		if strings.Contains(strings.ToLower(d.Description), "push the") {
			t.Errorf("tool %q advertises pushing", d.Name)
		}
	}
}

func TestReadFile(t *testing.T) {
	ctx, _ := newTestContext(t)
	res := run(t, ctx, "read_file", map[string]any{"path": "main.go"})
	if !strings.Contains(res.Content, "func main()") {
		t.Errorf("content = %q", res.Content)
	}
	if !strings.Contains(res.Content, "1\t") {
		t.Errorf("line numbers missing: %q", res.Content)
	}
}

func TestReadFileRange(t *testing.T) {
	ctx, _ := newTestContext(t)
	res := run(t, ctx, "read_file", map[string]any{"path": "main.go", "offset": 3, "limit": 1})
	if strings.Contains(res.Content, "package main") {
		t.Errorf("offset ignored: %q", res.Content)
	}
	if !strings.Contains(res.Content, "func main") {
		t.Errorf("expected line 3: %q", res.Content)
	}
}

func TestReadFileErrors(t *testing.T) {
	ctx, _ := newTestContext(t)
	if err := runErr(t, ctx, "read_file", map[string]any{"path": "missing.go"}); err == nil {
		t.Error("expected an error for a missing file")
	}
	if err := runErr(t, ctx, "read_file", map[string]any{}); err == nil {
		t.Error("expected an error when the path argument is missing")
	}
	if err := runErr(t, ctx, "read_file", map[string]any{"path": 42}); err == nil {
		t.Error("expected a type error")
	}
}

func TestReadFileRefusesOutsideWorkspace(t *testing.T) {
	ctx, _ := newTestContext(t)
	if _, err := ctx.Resolve("../escape.txt"); err == nil {
		t.Fatal("resolve should refuse paths outside the workspace")
	}
	if _, err := ctx.Resolve("/etc/passwd"); err == nil {
		t.Fatal("resolve should refuse absolute paths outside the workspace")
	}
}

func TestWriteFileCreatesAndRecordsJournal(t *testing.T) {
	ctx, dir := newTestContext(t)
	res := run(t, ctx, "write_file", map[string]any{
		"path":    "pkg/new.go",
		"content": "package pkg\n\nfunc New() {}\n",
	})
	if !strings.Contains(res.Content, "Created pkg/new.go") {
		t.Errorf("result = %q", res.Content)
	}
	if res.Diff == nil || res.Diff.Stats.Added != 3 {
		t.Errorf("diff = %+v", res.Diff)
	}
	data, err := os.ReadFile(filepath.Join(dir, "pkg/new.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "func New") {
		t.Error("file content wrong")
	}
	if ctx.Journal.UndoCount() != 1 {
		t.Errorf("journal entries = %d", ctx.Journal.UndoCount())
	}
	entries := ctx.Journal.Entries()
	if entries[0].BeforeExisted {
		t.Error("a new file must be recorded as not existing before")
	}
}

func TestWriteFileOverwriteRecordsBefore(t *testing.T) {
	ctx, _ := newTestContext(t)
	run(t, ctx, "write_file", map[string]any{"path": "main.go", "content": "package main\n"})
	entry := ctx.Journal.Entries()[0]
	if !entry.BeforeExisted {
		t.Error("overwrite must record the previous content")
	}
	if !strings.Contains(entry.Before, "func main") {
		t.Errorf("before content = %q", entry.Before)
	}
}

func TestEditFile(t *testing.T) {
	ctx, dir := newTestContext(t)
	res := run(t, ctx, "edit_file", map[string]any{
		"path":       "parser.go",
		"old_string": "return len(s)",
		"new_string": "return len(s) * 2",
	})
	if !strings.Contains(res.Content, "1 replacement") {
		t.Errorf("result = %q", res.Content)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "parser.go"))
	if !strings.Contains(string(data), "len(s) * 2") {
		t.Errorf("edit not applied: %q", data)
	}
	if res.Diff == nil || res.Diff.Stats.Deleted != 1 || res.Diff.Stats.Added != 1 {
		t.Errorf("diff stats = %+v", res.Diff.Stats)
	}
}

func TestEditFileAmbiguousMatch(t *testing.T) {
	ctx, _ := newTestContext(t)
	run(t, ctx, "write_file", map[string]any{"path": "dup.go", "content": "x\nsame\nx\nsame\n"})
	err := runErr(t, ctx, "edit_file", map[string]any{
		"path": "dup.go", "old_string": "same", "new_string": "different",
	})
	if err == nil || !strings.Contains(err.Error(), "appears 2 times") {
		t.Errorf("expected an ambiguity error, got %v", err)
	}
	res := run(t, ctx, "edit_file", map[string]any{
		"path": "dup.go", "old_string": "same", "new_string": "different", "replace_all": true,
	})
	if !strings.Contains(res.Content, "2 replacements") {
		t.Errorf("result = %q", res.Content)
	}
}

func TestEditFileNotFound(t *testing.T) {
	ctx, _ := newTestContext(t)
	err := runErr(t, ctx, "edit_file", map[string]any{
		"path": "main.go", "old_string": "does not exist here", "new_string": "x",
	})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected a not-found error, got %v", err)
	}
	err = runErr(t, ctx, "edit_file", map[string]any{"path": "nope.go", "old_string": "a", "new_string": "b"})
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("expected a missing-file error, got %v", err)
	}
}

func TestDeleteFile(t *testing.T) {
	ctx, dir := newTestContext(t)
	res := run(t, ctx, "delete_file", map[string]any{"path": "parser.go"})
	if !strings.Contains(res.Content, "Deleted parser.go") {
		t.Errorf("result = %q", res.Content)
	}
	if _, err := os.Stat(filepath.Join(dir, "parser.go")); !os.IsNotExist(err) {
		t.Error("file still exists")
	}
	if err := runErr(t, ctx, "delete_file", map[string]any{"path": "."}); err == nil {
		t.Error("delete_file must refuse the workspace root")
	}
}

func TestDeleteDirectoryRequiresRecursive(t *testing.T) {
	ctx, dir := newTestContext(t)
	if err := os.MkdirAll(filepath.Join(dir, "tree/inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runErr(t, ctx, "delete_file", map[string]any{"path": "tree"}); err == nil {
		t.Error("expected an error for a non-empty directory")
	}
	if err := runErr(t, ctx, "delete_file", map[string]any{"path": "tree", "recursive": true}); err != nil {
		t.Errorf("recursive delete failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "tree")); !os.IsNotExist(err) {
		t.Error("directory still exists")
	}
}

func TestListDirectory(t *testing.T) {
	ctx, _ := newTestContext(t)
	res := run(t, ctx, "list_directory", map[string]any{"path": "."})
	for _, want := range []string{"main.go", "README.md", "go.mod"} {
		if !strings.Contains(res.Content, want) {
			t.Errorf("missing %q in:\n%s", want, res.Content)
		}
	}
	if err := runErr(t, ctx, "list_directory", map[string]any{"path": "main.go"}); err == nil {
		t.Error("list_directory on a file should fail")
	}
}

func TestSearchFilesAndText(t *testing.T) {
	ctx, _ := newTestContext(t)
	files := run(t, ctx, "search_files", map[string]any{"pattern": "*.go"})
	if !strings.Contains(files.Content, "parser.go") {
		t.Errorf("files = %q", files.Content)
	}
	text := run(t, ctx, "search_text", map[string]any{"query": "func parse"})
	if !strings.Contains(text.Content, "parser.go:3") {
		t.Errorf("text search = %q", text.Content)
	}
	regex := run(t, ctx, "search_text", map[string]any{"query": `func\s+\w+\(`, "regex": true})
	if !strings.Contains(regex.Content, "main.go:3") {
		t.Errorf("regex search = %q", regex.Content)
	}
	none := run(t, ctx, "search_text", map[string]any{"query": "zzz-not-present"})
	if !strings.Contains(none.Content, "No matches") {
		t.Errorf("expected no matches message: %q", none.Content)
	}
}

func TestSearchTextInvalidRegex(t *testing.T) {
	ctx, _ := newTestContext(t)
	err := runErr(t, ctx, "search_text", map[string]any{"query": "([", "regex": true})
	if err == nil || !strings.Contains(err.Error(), "regular expression") {
		t.Errorf("expected a regex error, got %v", err)
	}
}

func TestInspectProject(t *testing.T) {
	ctx, _ := newTestContext(t)
	res := run(t, ctx, "inspect_project", map[string]any{})
	if !strings.Contains(res.Content, "Language: Go") {
		t.Errorf("language not detected:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "Package manager: go") {
		t.Errorf("package manager not detected:\n%s", res.Content)
	}
	if !strings.Contains(res.Content, "go test ./...") {
		t.Errorf("test command missing:\n%s", res.Content)
	}
	tree := run(t, ctx, "inspect_project", map[string]any{"include_tree": true})
	if !strings.Contains(tree.Content, "main.go") {
		t.Errorf("tree missing:\n%s", tree.Content)
	}
}

func TestListSymbols(t *testing.T) {
	ctx, _ := newTestContext(t)
	res := run(t, ctx, "list_symbols", map[string]any{"path": "main.go"})
	if !strings.Contains(res.Content, "main") {
		t.Errorf("symbols = %q", res.Content)
	}
	if err := runErr(t, ctx, "list_symbols", map[string]any{"path": "nope.go"}); err == nil {
		t.Error("expected an error for a file outside the index")
	}
}

func TestApplyPatch(t *testing.T) {
	ctx, dir := newTestContext(t)
	patch := `@@ -1,5 +1,6 @@
 package main
 
 func main() {
-	println("hi")
+	println("hello")
+	// done
 }
`
	res := run(t, ctx, "apply_patch", map[string]any{"path": "main.go", "patch": patch})
	if !strings.Contains(res.Content, "Applied the patch") {
		t.Errorf("result = %q", res.Content)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(data), "hello") || !strings.Contains(string(data), "// done") {
		t.Errorf("patch not applied:\n%s", data)
	}
}

func TestApplyPatchRejectsMismatchedContext(t *testing.T) {
	ctx, dir := newTestContext(t)
	patch := "@@ -1,2 +1,2 @@\n package wrong\n-nothing\n+something\n"
	err := runErr(t, ctx, "apply_patch", map[string]any{"path": "main.go", "patch": patch})
	if err == nil {
		t.Fatal("expected a context mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("error = %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(data), "func main") {
		t.Error("file must not be modified when the patch fails")
	}
}

func TestRunCommand(t *testing.T) {
	ctx, _ := newTestContext(t)
	res := run(t, ctx, "run_command", map[string]any{"command": "echo hello-talon"})
	if !strings.Contains(res.Content, "hello-talon") {
		t.Errorf("output = %q", res.Content)
	}
	if !strings.Contains(res.Content, "exit code 0") {
		t.Errorf("status missing: %q", res.Content)
	}
}

func TestRunCommandFailureIsNotAToolError(t *testing.T) {
	ctx, _ := newTestContext(t)
	res := run(t, ctx, "run_command", map[string]any{"command": "exit 3"})
	if !strings.Contains(res.Content, "exit code 3") {
		t.Errorf("content = %q", res.Content)
	}
	if code, _ := res.Metadata["exit_code"].(int); code != 3 {
		t.Errorf("metadata = %v", res.Metadata)
	}
}

func TestRunCommandStreamsOutput(t *testing.T) {
	ctx, _ := newTestContext(t)
	var chunks []string
	ctx.OnCommandOutput = func(stream, chunk string) { chunks = append(chunks, chunk) }
	run(t, ctx, "run_command", map[string]any{"command": "echo streamed"})
	if len(chunks) == 0 {
		t.Error("no output streamed to the UI")
	}
}

func TestRunTestsUsesDetectedCommand(t *testing.T) {
	ctx, _ := newTestContext(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	res := run(t, ctx, "run_tests", map[string]any{})
	if !strings.Contains(res.Content, "Test command: go test ./...") {
		t.Errorf("detected command not used: %q", res.Content)
	}
}

func TestBuildProject(t *testing.T) {
	ctx, _ := newTestContext(t)
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	res := run(t, ctx, "build_project", map[string]any{})
	if !strings.Contains(res.Content, "Build command: go build ./...") {
		t.Errorf("content = %q", res.Content)
	}
}

func TestGitToolsInNonRepo(t *testing.T) {
	ctx, _ := newTestContext(t)
	for _, name := range []string{"git_status", "git_diff", "git_log", "git_branch"} {
		err := runErr(t, ctx, name, map[string]any{})
		if err == nil || !strings.Contains(err.Error(), "not a git repository") {
			t.Errorf("%s: expected a not-a-repository error, got %v", name, err)
		}
	}
}

func TestGitToolsInRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx, dir := newTestContext(t)
	setup := func(args ...string) {
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
	setup("init", "-q")
	setup("add", ".")
	setup("commit", "-qm", "initial commit")

	status := run(t, ctx, "git_status", map[string]any{})
	if !strings.Contains(status.Content, "clean") {
		t.Errorf("status = %q", status.Content)
	}

	run(t, ctx, "edit_file", map[string]any{
		"path": "main.go", "old_string": "hi", "new_string": "hello",
	})
	status = run(t, ctx, "git_status", map[string]any{})
	if !strings.Contains(status.Content, "main.go") || !strings.Contains(status.Content, "modified") {
		t.Errorf("status after edit = %q", status.Content)
	}

	diffRes := run(t, ctx, "git_diff", map[string]any{})
	if !strings.Contains(diffRes.Content, "-\tprintln(\"hi\")") {
		t.Errorf("diff = %q", diffRes.Content)
	}

	run(t, ctx, "write_file", map[string]any{"path": "extra.go", "content": "package main\n"})
	commit := run(t, ctx, "git_commit", map[string]any{"message": "add extra", "paths": []string{"extra.go"}})
	if !strings.Contains(commit.Content, "Created commit") {
		t.Errorf("commit = %q", commit.Content)
	}
	if !strings.Contains(commit.Content, "never push") && !strings.Contains(commit.Content, "local") {
		t.Errorf("commit result should mention that nothing was pushed: %q", commit.Content)
	}

	logs := run(t, ctx, "git_log", map[string]any{"limit": 5})
	if !strings.Contains(logs.Content, "add extra") {
		t.Errorf("log = %q", logs.Content)
	}

	branches := run(t, ctx, "git_branch", map[string]any{})
	if !strings.Contains(branches.Content, "* ") {
		t.Errorf("branches = %q", branches.Content)
	}

	checkout := run(t, ctx, "git_checkout", map[string]any{"branch": "feature-x", "create": true})
	if !strings.Contains(checkout.Content, "feature-x") {
		t.Errorf("checkout = %q", checkout.Content)
	}
}

func TestGitCommitRequiresMessage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	ctx, dir := newTestContext(t)
	cmd := exec.Command("git", "init", "-q")
	cmd.Dir = dir
	if err := cmd.Run(); err != nil {
		t.Skip("git init failed")
	}
	if err := runErr(t, ctx, "git_commit", map[string]any{"message": "  "}); err == nil {
		t.Error("expected an error for an empty commit message")
	}
}

func TestPermissionRequestsCarryPathsAndCommands(t *testing.T) {
	r := newRegistryWithBuiltins()
	edit, _ := r.Get("edit_file")
	req := edit.Request(json.RawMessage(`{"path":"src/a.go","old_string":"x","new_string":"y"}`))
	if req.Risk != perm.RiskWrite {
		t.Errorf("risk = %v", req.Risk)
	}
	if req.Path != "src/a.go" {
		t.Errorf("path = %q", req.Path)
	}
	if !strings.Contains(req.Description, "src/a.go") {
		t.Errorf("description = %q", req.Description)
	}

	cmd, _ := r.Get("run_command")
	req = cmd.Request(json.RawMessage(`{"command":"rm -rf /"}`))
	if req.Risk != perm.RiskExec || req.Command != "rm -rf /" {
		t.Errorf("request = %+v", req)
	}
	if !strings.Contains(req.Description, "rm -rf /") {
		t.Errorf("description = %q", req.Description)
	}

	read, _ := r.Get("read_file")
	if read.Request(json.RawMessage(`{"path":"a.go"}`)).Risk != perm.RiskRead {
		t.Error("read_file should be read-only")
	}
	del, _ := r.Get("delete_file")
	if del.Request(json.RawMessage(`{"path":"a.go"}`)).Risk != perm.RiskDanger {
		t.Error("delete_file must be classified as dangerous")
	}
}

func TestValidateSchema(t *testing.T) {
	schema := llm.JSONSchema(map[string]any{
		"path":    llm.Prop("string", "p"),
		"count":   map[string]any{"type": "integer", "minimum": 1},
		"mode":    llm.Enum("m", "read", "write"),
		"verbose": map[string]any{"type": "boolean"},
	}, "path")

	if err := Validate(schema, map[string]any{"path": "a", "count": 3}); err != nil {
		t.Errorf("valid args rejected: %v", err)
	}
	if err := Validate(schema, map[string]any{}); err == nil {
		t.Error("missing required argument should fail")
	}
	if err := Validate(schema, map[string]any{"path": "a", "count": 1.5}); err == nil {
		t.Error("non-integer should fail")
	}
	if err := Validate(schema, map[string]any{"path": "a", "count": 0}); err == nil {
		t.Error("below minimum should fail")
	}
	if err := Validate(schema, map[string]any{"path": "a", "mode": "sideways"}); err == nil {
		t.Error("outside the enum should fail")
	}
	if err := Validate(schema, map[string]any{"path": 1}); err == nil {
		t.Error("wrong type should fail")
	}
	if err := Validate(schema, map[string]any{"path": "a", "verbose": true}); err != nil {
		t.Errorf("boolean rejected: %v", err)
	}
}

func TestSpecMatchesDefinition(t *testing.T) {
	r := newRegistryWithBuiltins()
	specs := r.Specs()
	if len(specs) != r.Len() {
		t.Fatalf("specs = %d, tools = %d", len(specs), r.Len())
	}
	for _, s := range specs {
		if s.Name == "" || s.Description == "" || s.Parameters == nil {
			t.Errorf("bad spec: %+v", s)
		}
	}
}

func TestOutputIsTruncated(t *testing.T) {
	ctx, dir := newTestContext(t)
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Repeat("line of text\n", 5000)), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx.Limits.MaxOutputBytes = 500
	res := run(t, ctx, "read_file", map[string]any{"path": "big.txt"})
	if len(res.Content) > 700 {
		t.Errorf("output not truncated: %d bytes", len(res.Content))
	}
	if !strings.Contains(res.Content, "truncated") {
		t.Errorf("truncation not announced: %q", res.Content[len(res.Content)-80:])
	}
}

func TestNonObjectArgumentsRejected(t *testing.T) {
	ctx, _ := newTestContext(t)
	def, _ := newRegistryWithBuiltins().Get("read_file")
	if _, err := def.Run(ctx, json.RawMessage(`["a"]`)); err == nil {
		t.Error("expected an error for non-object arguments")
	}
	if _, err := def.Run(ctx, json.RawMessage(`{`)); err == nil {
		t.Error("expected an error for invalid JSON")
	}
}
