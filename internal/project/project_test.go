package project

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// writeProject materialises a project from a map of files.
func writeProject(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetectGoProject(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"go.mod":            "module example.com/x\n\ngo 1.22\n",
		"main.go":           "package main\n\nfunc main() {}\n",
		"pkg/thing.go":      "package pkg\n",
		"pkg/thing_test.go": "package pkg\n",
		"README.md":         "# x\n",
	})
	info := Detect(context.Background(), dir)
	if info.Primary != "Go" {
		t.Errorf("primary = %q (%v)", info.Primary, info.Languages)
	}
	if info.BuildCommand != "go build ./..." || info.TestCommand != "go test ./..." {
		t.Errorf("commands = %q / %q", info.BuildCommand, info.TestCommand)
	}
	if info.PackageManager == nil || info.PackageManager.Name != "go" {
		t.Errorf("package manager = %+v", info.PackageManager)
	}
	if len(info.TestDirs) == 0 && info.LanguageCounts["Go"] < 3 {
		t.Errorf("test layout not detected: %+v", info)
	}
	if info.FileCount < 3 {
		t.Errorf("file count = %d", info.FileCount)
	}
	if info.LintCommand != "go vet ./..." {
		t.Errorf("lint = %q", info.LintCommand)
	}
	desc := info.Describe()
	for _, want := range []string{"Language: Go", "Build:", "Tests:", "README:"} {
		if !strings.Contains(desc, want) {
			t.Errorf("Describe missing %q:\n%s", want, desc)
		}
	}
}

func TestDetectRustProject(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"Cargo.toml":  "[package]\nname = \"x\"\n",
		"Cargo.lock":  "",
		"src/main.rs": "fn main() {}\n",
		"src/lib.rs":  "pub fn thing() {}\n",
		"tests/it.rs": "#[test]\nfn works() {}\n",
	})
	info := Detect(context.Background(), dir)
	if info.Primary != "Rust" {
		t.Errorf("primary = %q", info.Primary)
	}
	if info.BuildCommand != "cargo build" || info.TestCommand != "cargo test" {
		t.Errorf("commands = %q / %q", info.BuildCommand, info.TestCommand)
	}
	if info.PackageManager == nil || info.PackageManager.Name != "cargo" {
		t.Errorf("package manager = %+v", info.PackageManager)
	}
	if len(info.EntryPoints) == 0 {
		t.Error("no entry point found")
	}
}

func TestDetectPythonProject(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"pyproject.toml":    "[project]\nname = \"x\"\ndependencies = [\"fastapi\", \"pytest\"]\n",
		"src/app.py":        "def main():\n    pass\n",
		"tests/test_app.py": "def test_x():\n    pass\n",
		"requirements.txt":  "fastapi\n",
	})
	info := Detect(context.Background(), dir)
	if info.Primary != "Python" {
		t.Errorf("primary = %q", info.Primary)
	}
	if !containsString(info.Frameworks, "fastapi") {
		t.Errorf("frameworks = %v", info.Frameworks)
	}
	if info.PackageManager == nil || info.PackageManager.Name != "pip" {
		t.Errorf("package manager = %+v", info.PackageManager)
	}
}

func TestDetectNodeProject(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"package.json": `{"name":"x","dependencies":{"react":"^18"},"devDependencies":{"jest":"^29"},` +
			`"scripts":{"build":"tsc","test":"jest"}}`,
		"pnpm-lock.yaml":    "",
		"src/index.ts":      "export const x = 1\n",
		"src/App.tsx":       "export const App = () => null\n",
		"src/index.test.ts": "test('x', () => {})\n",
	})
	info := Detect(context.Background(), dir)
	if info.Primary != "TypeScript" {
		t.Errorf("primary = %q (%v)", info.Primary, info.Languages)
	}
	if info.PackageManager == nil || info.PackageManager.Name != "pnpm" {
		t.Errorf("package manager = %+v", info.PackageManager)
	}
	if !containsString(info.Frameworks, "react") || !containsString(info.Frameworks, "jest") {
		t.Errorf("frameworks = %v", info.Frameworks)
	}
}

func TestDetectCWithCMake(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"CMakeLists.txt": "project(x)\n",
		"src/main.c":     "int main() { return 0; }\n",
		"src/util.h":     "#pragma once\n",
	})
	info := Detect(context.Background(), dir)
	if info.Primary != "C" && info.Primary != "C++" {
		t.Errorf("primary = %q", info.Primary)
	}
	if !containsString(info.Manifests, "CMakeLists.txt") {
		t.Errorf("manifests = %v", info.Manifests)
	}
}

func TestDetectJavaAndGradle(t *testing.T) {
	dir := writeProject(t, map[string]string{
		"build.gradle":            "plugins { id 'java' }\n",
		"src/main/java/Main.java": "class Main {}\n",
	})
	info := Detect(context.Background(), dir)
	if info.Primary != "Java" {
		t.Errorf("primary = %q", info.Primary)
	}
	if info.PackageManager == nil || info.PackageManager.Name != "gradle" {
		t.Errorf("package manager = %+v", info.PackageManager)
	}
}

func TestDetectEmptyDirectory(t *testing.T) {
	dir := t.TempDir()
	info := Detect(context.Background(), dir)
	if info.Primary != "" {
		t.Errorf("primary = %q", info.Primary)
	}
	if len(info.Warnings) < 2 {
		t.Errorf("warnings = %v", info.Warnings)
	}
	if !strings.Contains(info.Describe(), "Warning:") {
		t.Errorf("warnings not surfaced:\n%s", info.Describe())
	}
}

func TestDetectGitState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := writeProject(t, map[string]string{"main.go": "package main\n"})
	git := exec.Command("git", "init", "-q")
	git.Dir = dir
	if err := git.Run(); err != nil {
		t.Skip("git init failed")
	}
	info := Detect(context.Background(), dir)
	if !info.HasGit {
		t.Fatal("git not detected")
	}
	if info.GitBranch == "" {
		t.Error("branch not detected")
	}
	if info.GitRoot == "" {
		t.Error("root not detected")
	}
	if !strings.Contains(info.Describe(), "git: branch") {
		t.Errorf("git not in description:\n%s", info.Describe())
	}
}

func TestWalkRespectsGitignore(t *testing.T) {
	dir := writeProject(t, map[string]string{
		".gitignore":            "secret/\n*.log\n",
		"main.go":               "package main\n",
		"secret/key.go":         "package main\n",
		"debug.log":             "noise\n",
		"nested/keep.go":        "package nested\n",
		"node_modules/dep/x.js": "x\n",
	})
	var seen []string
	err := Walk(dir, func(path string, info os.FileInfo) bool {
		rel, _ := filepath.Rel(dir, path)
		seen = append(seen, filepath.ToSlash(rel))
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(seen, ",")
	if strings.Contains(joined, "secret/key.go") {
		t.Errorf(".gitignore did not exclude secret/: %v", seen)
	}
	if strings.Contains(joined, "debug.log") {
		t.Errorf(".gitignore did not exclude *.log: %v", seen)
	}
	if strings.Contains(joined, "node_modules") {
		t.Errorf("node_modules was walked: %v", seen)
	}
	if !strings.Contains(joined, "main.go") || !strings.Contains(joined, "nested/keep.go") {
		t.Errorf("real files were skipped: %v", seen)
	}
}

func TestWalkStopsWhenTheCallbackReturnsFalse(t *testing.T) {
	dir := writeProject(t, map[string]string{"a.go": "a", "b.go": "b"})
	count := 0
	_ = Walk(dir, func(path string, info os.FileInfo) bool {
		count++
		return count < 2
	})
	if count != 2 {
		t.Errorf("callback ran %d times, want 2", count)
	}
}

func TestLanguageOf(t *testing.T) {
	cases := map[string]string{
		"a.go":      "Go",
		"a.rs":      "Rust",
		"a.py":      "Python",
		"a.ts":      "TypeScript",
		"a.js":      "JavaScript",
		"a.cpp":     "C++",
		"a.kt":      "Kotlin",
		"a.swift":   "Swift",
		"a.cs":      "C#",
		"a.sh":      "Shell",
		"a.java":    "Java",
		"a.unknown": "",
	}
	for path, want := range cases {
		if got := LanguageOf(path); got != want {
			t.Errorf("LanguageOf(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestNormalizeLang(t *testing.T) {
	cases := map[string]string{
		"golang": "go", "rs": "rust", "py": "python", "sh": "shell",
		"c++": "cpp", "ts": "typescript", "yml": "yaml", "unknown": "unknown",
	}
	for in, want := range cases {
		if got := NormalizeLang(in); got != want {
			t.Errorf("NormalizeLang(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSortPaths(t *testing.T) {
	in := []string{"z/a.go", "a.go", "m/b/c.go", "m/b.go"}
	SortPaths(in)
	if in[0] != "a.go" {
		t.Errorf("sorted = %v", in)
	}
	// Shallower paths must come first.
	if strings.Count(in[0], "/") != 0 {
		t.Errorf("depth ordering failed: %v", in)
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
