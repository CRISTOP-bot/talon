package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) *Index {
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
	write("main.go", "package main\n\nfunc main() { helper() }\n\nfunc helper() {}\n")
	write("pkg/parser.go", "package pkg\n\nfunc Parse(s string) error {\n\treturn nil\n}\n\ntype Parser struct{}\n")
	write("pkg/parser_test.go", "package pkg\n\nfunc TestParse(t *testing.T) {}\n")
	write("README.md", "# fixture\n")
	write("web/app.ts", "export function render() { return 1 }\n")
	write("pkg/user.go", "package pkg\n\n// parser builds a parser\nfunc user() {}\n")
	write("node_modules/left-pad/index.js", "module.exports = 1\n")
	write(".git/config", "[core]\n")
	write("dist/bundle.js", "compiled\n")

	ix := New(dir)
	if err := ix.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	return ix
}

func TestBuildSkipsIgnoredDirectories(t *testing.T) {
	ix := fixture(t)
	paths := ix.FindFiles("*.js", 0)
	for _, p := range paths {
		if p == "node_modules/left-pad/index.js" || p == "dist/bundle.js" {
			t.Errorf("ignored file indexed: %s", p)
		}
	}
	if len(ix.FindFiles("*", 0)) == 0 {
		t.Error("index is empty")
	}
}

func TestStatsAndLanguages(t *testing.T) {
	ix := fixture(t)
	stats := ix.Stats()
	if stats.Files == 0 {
		t.Fatal("no files indexed")
	}
	if stats.Languages["Go"] < 3 {
		t.Errorf("languages = %v", stats.Languages)
	}
	if stats.Languages["TypeScript"] != 1 {
		t.Errorf("typescript not detected: %v", stats.Languages)
	}
	if stats.Tests != 1 {
		t.Errorf("test files = %d", stats.Tests)
	}
	if stats.BuiltAt.IsZero() {
		t.Error("build time not recorded")
	}
	if ix.Len() != stats.Files {
		t.Errorf("Len = %d, stats = %d", ix.Len(), stats.Files)
	}
}

func TestSymbolsAreExtracted(t *testing.T) {
	ix := fixture(t)
	entries := ix.FindSymbols("Parse", 0)
	if len(entries) == 0 {
		t.Fatal("Parse not found")
	}
	found := false
	for _, sym := range entries[0].Symbols {
		if sym == "Parse" || sym == "Parser" {
			found = true
		}
	}
	if !found {
		t.Errorf("symbols = %v", entries[0].Symbols)
	}
}

func TestSearchPlainAndRegex(t *testing.T) {
	ix := fixture(t)
	matches, err := ix.Search(context.Background(), SearchOptions{Query: "helper"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 || matches[0].Path != "main.go" {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].Line != 3 {
		t.Errorf("line = %d", matches[0].Line)
	}
	regex, err := ix.Search(context.Background(), SearchOptions{Query: `func\s+\w+`, Regex: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(regex) < 3 {
		t.Errorf("regex matches = %d", len(regex))
	}
	if _, err := ix.Search(context.Background(), SearchOptions{Query: "([", Regex: true}); err == nil {
		t.Error("expected an error for an invalid regex")
	}
	if _, err := ix.Search(context.Background(), SearchOptions{Query: ""}); err == nil {
		t.Error("an empty query should fail")
	}
}

func TestSearchGlobAndCase(t *testing.T) {
	ix := fixture(t)
	matches, _ := ix.Search(context.Background(), SearchOptions{Query: "render", Glob: "web/*.ts"})
	if len(matches) != 1 {
		t.Errorf("glob filter failed: %+v", matches)
	}
	none, _ := ix.Search(context.Background(), SearchOptions{Query: "render", Glob: "pkg/*.go"})
	if len(none) != 0 {
		t.Errorf("glob should have excluded everything: %+v", none)
	}
	upper, _ := ix.Search(context.Background(), SearchOptions{Query: "TESTPARSE", CaseSensitive: true, IncludeTests: true})
	if len(upper) != 0 {
		t.Errorf("case sensitivity ignored: %+v", upper)
	}
	insensitive, _ := ix.Search(context.Background(), SearchOptions{Query: "testparse", IncludeTests: true})
	if len(insensitive) == 0 {
		t.Error("search should be case-insensitive by default")
	}
}

func TestSearchLimit(t *testing.T) {
	ix := fixture(t)
	matches, _ := ix.Search(context.Background(), SearchOptions{Query: "e", MaxResults: 2})
	if len(matches) > 2 {
		t.Errorf("limit ignored: %d matches", len(matches))
	}
}

func TestExcludeTests(t *testing.T) {
	ix := fixture(t)
	inc, _ := ix.Search(context.Background(), SearchOptions{Query: "testing.T", IncludeTests: true})
	exc, _ := ix.Search(context.Background(), SearchOptions{Query: "testing.T", IncludeTests: false})
	if len(inc) == 0 {
		t.Skip("fixture does not exercise test files")
	}
	if len(exc) != 0 {
		t.Errorf("test files were not excluded: %+v", exc)
	}
}

func TestFindFilesByNameAndSubstring(t *testing.T) {
	ix := fixture(t)
	if got := ix.FindFiles("*.go", 0); len(got) < 3 {
		t.Errorf("glob search = %v", got)
	}
	if got := ix.FindFiles("parser", 0); len(got) != 2 {
		t.Errorf("substring search = %v", got)
	}
	if got := ix.FindFiles("*.go", 1); len(got) != 1 {
		t.Errorf("limit ignored: %v", got)
	}
	if got := ix.FindFiles("nothing-matches-this", 0); len(got) != 0 {
		t.Errorf("unexpected results: %v", got)
	}
}

func TestRelatedFiles(t *testing.T) {
	ix := fixture(t)
	related := ix.Related("pkg/parser.go", 5)
	var hasSibling, hasOther bool
	for _, r := range related {
		if r == "pkg/parser_test.go" || r == "main.go" {
			hasSibling = true
		}
		if r == "pkg/user.go" {
			hasOther = true
		}
	}
	if !hasSibling {
		t.Errorf("sibling not suggested: %v", related)
	}
	if !hasOther {
		t.Errorf("cross-reference not suggested: %v", related)
	}
	for _, r := range related {
		if r == "pkg/parser.go" {
			t.Error("the file itself must not be suggested")
		}
	}
}

func TestIncrementalRebuildKeepsEntries(t *testing.T) {
	ix := fixture(t)
	before := ix.Stats()
	if err := ix.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ix.Stats().Files != before.Files {
		t.Error("rebuilding changed the file count")
	}
	if err := os.WriteFile(filepath.Join(ix.Root(), "extra.go"),
		[]byte("package main\n\nfunc extra() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ix.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ix.Stats().Files != before.Files+1 {
		t.Errorf("new file not indexed: %d -> %d", before.Files, ix.Stats().Files)
	}
	if len(ix.FindSymbols("extra", 0)) == 0 {
		t.Error("symbols of the new file were not extracted")
	}
}
