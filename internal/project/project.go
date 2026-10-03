// Package project detects what a project is: language, framework, build system,
// package manager, test layout and git state. The result seeds the agent's
// system prompt and the inspect_project tool.
package project

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/talon-cli/talon/internal/git"
)

// Language is a detected programming language.
type Language struct {
	Name       string
	Extensions []string
	// Manifests are files that indicate the language.
	Manifests []string
	// BuildCommands are the usual build/test invocations.
	BuildCommand string
	TestCommand  string
}

// languages maps a language name to its detection rules.
var languages = []Language{
	{Name: "Rust", Extensions: []string{".rs"}, Manifests: []string{"Cargo.toml"},
		BuildCommand: "cargo build", TestCommand: "cargo test"},
	{Name: "Go", Extensions: []string{".go"}, Manifests: []string{"go.mod"},
		BuildCommand: "go build ./...", TestCommand: "go test ./..."},
	{Name: "Python", Extensions: []string{".py"}, Manifests: []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt", "Pipfile"},
		BuildCommand: "python -m compileall .", TestCommand: "python -m pytest"},
	{Name: "TypeScript", Extensions: []string{".ts", ".tsx"}, Manifests: []string{"tsconfig.json"},
		BuildCommand: "npm run build", TestCommand: "npm test"},
	{Name: "JavaScript", Extensions: []string{".js", ".jsx", ".mjs", ".cjs"}, Manifests: []string{"package.json"},
		BuildCommand: "npm run build", TestCommand: "npm test"},
	{Name: "C", Extensions: []string{".c", ".h"}, Manifests: []string{"Makefile", "CMakeLists.txt"},
		BuildCommand: "make", TestCommand: "make test"},
	{Name: "C++", Extensions: []string{".cpp", ".cc", ".cxx", ".hpp", ".hh"}, Manifests: []string{"CMakeLists.txt", "Makefile"},
		BuildCommand: "cmake --build build", TestCommand: "ctest --test-dir build"},
	{Name: "Java", Extensions: []string{".java"}, Manifests: []string{"pom.xml", "build.gradle", "build.gradle.kts"},
		BuildCommand: "mvn -q package", TestCommand: "mvn -q test"},
	{Name: "Kotlin", Extensions: []string{".kt", ".kts"}, Manifests: []string{"build.gradle.kts", "build.gradle"},
		BuildCommand: "./gradlew build", TestCommand: "./gradlew test"},
	{Name: "Swift", Extensions: []string{".swift"}, Manifests: []string{"Package.swift"},
		BuildCommand: "swift build", TestCommand: "swift test"},
	{Name: "C#", Extensions: []string{".cs"}, Manifests: []string{"talon.csproj", "*.csproj", "*.sln"},
		BuildCommand: "dotnet build", TestCommand: "dotnet test"},
	{Name: "Shell", Extensions: []string{".sh", ".bash", ".zsh"}, Manifests: []string{},
		BuildCommand: "sh -n", TestCommand: "shellcheck"},
	{Name: "Ruby", Extensions: []string{".rb"}, Manifests: []string{"Gemfile", "*.gemspec"},
		BuildCommand: "bundle install", TestCommand: "bundle exec rspec"},
	{Name: "PHP", Extensions: []string{".php"}, Manifests: []string{"composer.json"},
		BuildCommand: "composer install", TestCommand: "phpunit"},
	{Name: "Dart", Extensions: []string{".dart"}, Manifests: []string{"pubspec.yaml"},
		BuildCommand: "dart compile exe", TestCommand: "dart test"},
	{Name: "Zig", Extensions: []string{".zig"}, Manifests: []string{"build.zig"},
		BuildCommand: "zig build", TestCommand: "zig build test"},
	{Name: "Elixir", Extensions: []string{".ex", ".exs"}, Manifests: []string{"mix.exs"},
		BuildCommand: "mix compile", TestCommand: "mix test"},
	{Name: "Lua", Extensions: []string{".lua"}, Manifests: []string{},
		BuildCommand: "luac -p", TestCommand: "busted"},
}

// PackageManager describes the detected package manager.
type PackageManager struct {
	Name      string
	Lockfile  string
	Install   string
	Add       string
	Run       string
	Test      string
	Available bool
}

// Info is the full description of a project.
type Info struct {
	Root           string
	Primary        string
	Languages      []string
	LanguageCounts map[string]int
	Frameworks     []string
	PackageManager *PackageManager
	BuildCommand   string
	TestCommand    string
	LintCommand    string
	HasGit         bool
	GitRoot        string
	GitBranch      string
	ModifiedFiles  int
	UntrackedFiles int
	Manifests      []string
	EntryPoints    []string
	TestDirs       []string
	SourceDirs     []string
	ReadmePath     string
	DocDirs        []string
	LineCount      int
	FileCount      int
	Warnings       []string
}

// Detect inspects root and returns the project description.
func Detect(ctx context.Context, root string) *Info {
	info := &Info{
		Root:           root,
		LanguageCounts: map[string]int{},
	}
	info.Languages, info.LanguageCounts = detectLanguages(root)
	info.Primary = primaryLanguage(info)
	for _, lang := range languages {
		if lang.Name == info.Primary {
			info.BuildCommand = lang.BuildCommand
			info.TestCommand = lang.TestCommand
			break
		}
	}
	info.Manifests = findManifests(root)
	info.Frameworks = detectFrameworks(root)
	info.PackageManager = detectPackageManager(root)
	info.HasGit = git.Available() && git.New(root).IsRepo(ctx)
	if info.HasGit {
		info.GitRoot = git.RepoRoot(ctx, root)
		if branch, err := git.New(root).CurrentBranch(ctx); err == nil {
			info.GitBranch = branch
		}
		if modified, _, _, untracked, err := git.New(root).StatusSummary(ctx); err == nil {
			info.ModifiedFiles = modified
			info.UntrackedFiles = untracked
		}
	}
	info.SourceDirs, info.TestDirs = detectLayout(root)
	info.EntryPoints = detectEntryPoints(root, info)
	info.ReadmePath, info.DocDirs = detectDocs(root)
	info.LintCommand = detectLint(root, info)
	info.FileCount, info.LineCount = countCode(root)
	if info.FileCount == 0 {
		info.Warnings = append(info.Warnings, "no source files were found")
	}
	if !info.HasGit {
		info.Warnings = append(info.Warnings, "this directory is not a git repository")
	}
	if info.Primary == "" {
		info.Warnings = append(info.Warnings, "the language could not be determined")
	}
	return info
}

// maxScanFiles bounds how many files are inspected during detection.
const maxScanFiles = 4000

func detectLanguages(root string) ([]string, map[string]int) {
	counts := map[string]int{}
	byExt := map[string]bool{}
	extToLang := map[string]string{}
	for _, l := range languages {
		for _, e := range l.Extensions {
			extToLang[e] = l.Name
		}
	}
	_ = Walk(root, func(path string, info os.FileInfo) bool {
		ext := strings.ToLower(filepath.Ext(path))
		if lang, ok := extToLang[ext]; ok {
			counts[lang]++
			byExt[ext] = true
		}
		return true
	})
	var langs []string
	for l, n := range counts {
		langs = append(langs, l)
		_ = n
	}
	sort.Slice(langs, func(i, j int) bool { return counts[langs[i]] > counts[langs[j]] })
	return langs, counts
}

func primaryLanguage(info *Info) string {
	if len(info.Languages) > 0 {
		return info.Languages[0]
	}
	return ""
}

func findManifests(root string) []string {
	known := []string{
		"Cargo.toml", "go.mod", "package.json", "pyproject.toml", "setup.py",
		"requirements.txt", "pom.xml", "build.gradle", "build.gradle.kts",
		"CMakeLists.txt", "Makefile", "Package.swift", "composer.json",
		"mix.exs", "build.zig", "pubspec.yaml", "Gemfile", "deno.json",
		"tsconfig.json", "setup.cfg", "Pipfile", "flake.nix", "shell.nix",
	}
	var found []string
	for _, name := range known {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			found = append(found, name)
		}
	}
	// Any *.csproj / *.sln counts as a manifest too.
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".csproj") || strings.HasSuffix(name, ".sln") ||
			strings.HasSuffix(name, ".gemspec") {
			found = append(found, name)
		}
	}
	sort.Strings(found)
	return found
}

func detectFrameworks(root string) []string {
	seen := map[string]bool{}
	add := func(f string) {
		if !seen[f] {
			seen[f] = true
		}
	}
	readFile := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			return ""
		}
		return string(data)
	}
	pkg := readFile("package.json")
	for _, fw := range []string{"react", "next", "vue", "nuxt", "svelte", "express",
		"@nestjs/core", "fastify", "electron", "vite", "webpack", "tailwindcss",
		"@angular/core", "jest", "vitest"} {
		if strings.Contains(pkg, `"`+fw+`"`) {
			add(fw)
		}
	}
	cargo := readFile("Cargo.toml")
	for _, fw := range []string{"tokio", "axum", "actix-web", "rocket", "serde",
		"diesel", "sqlx", "tauri", "clap", "anyhow"} {
		if strings.Contains(cargo, fw) {
			add(fw)
		}
	}
	goMod := readFile("go.mod")
	for _, fw := range []string{"gin-gonic", "labstack/echo", "gofiber", "chi",
		"grpc", "spf13/cobra", "gorilla/mux", "testify"} {
		if strings.Contains(goMod, fw) {
			add(fw)
		}
	}
	py := readFile("pyproject.toml") + readFile("requirements.txt")
	for _, fw := range []string{"django", "flask", "fastapi", "pytest", "sqlalchemy",
		"pydantic", "numpy", "pandas", "torch", "click", "typer"} {
		if strings.Contains(strings.ToLower(py), fw) {
			add(fw)
		}
	}
	pom := readFile("pom.xml")
	for _, fw := range []string{"spring-boot", "junit", "guava"} {
		if strings.Contains(pom, fw) {
			add(fw)
		}
	}
	out := make([]string, 0, len(seen))
	for f := range seen {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func detectPackageManager(root string) *PackageManager {
	type candidate struct {
		pm PackageManager
	}
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(root, name))
		return err == nil
	}
	if binAvailable("bun") && has("bun.lockb") {
		return &PackageManager{Name: "bun", Lockfile: "bun.lockb", Install: "bun install", Add: "bun add", Run: "bun run", Test: "bun test", Available: true}
	}
	if has("pnpm-lock.yaml") {
		return &PackageManager{Name: "pnpm", Lockfile: "pnpm-lock.yaml", Install: "pnpm install", Add: "pnpm add", Run: "pnpm run", Test: "pnpm test", Available: binAvailable("pnpm")}
	}
	if has("yarn.lock") {
		return &PackageManager{Name: "yarn", Lockfile: "yarn.lock", Install: "yarn install", Add: "yarn add", Run: "yarn", Test: "yarn test", Available: binAvailable("yarn")}
	}
	if has("deno.lock") {
		return &PackageManager{Name: "deno", Lockfile: "deno.lock", Install: "deno install", Add: "deno add", Run: "deno task", Test: "deno test", Available: binAvailable("deno")}
	}
	if has("package.json") {
		return &PackageManager{Name: "npm", Lockfile: "package-lock.json", Install: "npm install", Add: "npm install", Run: "npm run", Test: "npm test", Available: binAvailable("npm")}
	}
	if has("Cargo.lock") || has("Cargo.toml") {
		return &PackageManager{Name: "cargo", Lockfile: "Cargo.lock", Install: "cargo fetch", Add: "cargo add", Run: "cargo run", Test: "cargo test", Available: binAvailable("cargo")}
	}
	if has("go.sum") || has("go.mod") {
		return &PackageManager{Name: "go", Lockfile: "go.sum", Install: "go mod download", Add: "go get", Run: "go run", Test: "go test", Available: binAvailable("go")}
	}
	if has("uv.lock") {
		return &PackageManager{Name: "uv", Lockfile: "uv.lock", Install: "uv sync", Add: "uv add", Run: "uv run", Test: "uv run pytest", Available: binAvailable("uv")}
	}
	if has("poetry.lock") {
		return &PackageManager{Name: "poetry", Lockfile: "poetry.lock", Install: "poetry install", Add: "poetry add", Run: "poetry run", Test: "poetry run pytest", Available: binAvailable("poetry")}
	}
	if has("Pipfile") {
		return &PackageManager{Name: "pipenv", Lockfile: "Pipfile.lock", Install: "pipenv install", Add: "pipenv install", Run: "pipenv run", Test: "pipenv run pytest", Available: binAvailable("pipenv")}
	}
	if has("requirements.txt") || has("pyproject.toml") {
		return &PackageManager{Name: "pip", Lockfile: "requirements.txt", Install: "pip install -r requirements.txt", Add: "pip install", Run: "python -m", Test: "pytest", Available: binAvailable("python3")}
	}
	if has("Gemfile.lock") || has("Gemfile") {
		return &PackageManager{Name: "bundler", Lockfile: "Gemfile.lock", Install: "bundle install", Add: "bundle add", Run: "bundle exec", Test: "bundle exec rspec", Available: binAvailable("bundle")}
	}
	if has("composer.lock") || has("composer.json") {
		return &PackageManager{Name: "composer", Lockfile: "composer.lock", Install: "composer install", Add: "composer require", Run: "composer run", Test: "phpunit", Available: binAvailable("composer")}
	}
	if has("pom.xml") {
		return &PackageManager{Name: "maven", Lockfile: "pom.xml", Install: "mvn -q dependency:resolve", Add: "mvn dependency:get", Run: "mvn -q exec:java", Test: "mvn -q test", Available: binAvailable("mvn")}
	}
	if has("gradlew") || has("build.gradle") || has("build.gradle.kts") {
		pm := &PackageManager{Name: "gradle", Lockfile: "build.gradle", Install: "gradle dependencies", Add: "gradle add", Run: "gradle", Test: "gradle test"}
		if has("gradlew") {
			pm.Install, pm.Add, pm.Run, pm.Test = "./gradlew dependencies", "./gradlew add", "./gradlew", "./gradlew test"
			pm.Available = true
		} else {
			pm.Available = binAvailable("gradle")
		}
		return pm
	}
	if has("mix.lock") || has("mix.exs") {
		return &PackageManager{Name: "mix", Lockfile: "mix.lock", Install: "mix deps.get", Add: "mix deps.get", Run: "mix", Test: "mix test", Available: binAvailable("mix")}
	}
	_ = candidate{}
	return nil
}

func detectLayout(root string) (sources, tests []string) {
	for _, dir := range []string{"src", "lib", "app", "cmd", "internal", "pkg", "source"} {
		if isDir(filepath.Join(root, dir)) {
			sources = append(sources, dir)
		}
	}
	for _, dir := range []string{"test", "tests", "spec", "__tests__", "testing"} {
		if isDir(filepath.Join(root, dir)) {
			tests = append(tests, dir)
		}
	}
	return sources, tests
}

func detectEntryPoints(root string, info *Info) []string {
	candidates := []string{
		"main.go", "cmd/talon/main.go", "src/main.rs", "src/main.ts", "src/main.js",
		"src/index.ts", "src/index.js", "index.ts", "index.js", "src/app.py",
		"main.py", "app.py", "manage.py", "__main__.py", "src/main.c", "main.c",
		"src/main.cpp", "main.cpp", "Main.java", "Sources/main.swift",
		"Program.cs", "src/index.zig",
	}
	var out []string
	for _, c := range candidates {
		if isFile(filepath.Join(root, filepath.FromSlash(c))) {
			out = append(out, c)
		}
	}
	if info.Primary == "Rust" && len(out) == 0 {
		if bin := findRustBinary(root); bin != "" {
			out = append(out, bin)
		}
	}
	return out
}

func findRustBinary(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "Cargo.toml"))
	if err != nil {
		return ""
	}
	inSrc := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "[[bin]]"):
			inSrc = true
		case inSrc && strings.HasPrefix(trimmed, "path"):
			parts := strings.Split(trimmed, "=")
			if len(parts) == 2 {
				return strings.Trim(strings.TrimSpace(parts[1]), `"`)
			}
		case trimmed == "":
			inSrc = false
		}
	}
	return ""
}

func detectDocs(root string) (readme string, docs []string) {
	for _, name := range []string{"README.md", "readme.md", "README.rst", "README.txt", "README"} {
		if isFile(filepath.Join(root, name)) {
			readme = name
			break
		}
	}
	for _, dir := range []string{"docs", "doc", "documentation"} {
		if isDir(filepath.Join(root, dir)) {
			docs = append(docs, dir)
		}
	}
	return readme, docs
}

func detectLint(root string, info *Info) string {
	if info.PackageManager != nil && info.PackageManager.Name == "npm" {
		if isFile(filepath.Join(root, "eslint.config.js")) || isFile(filepath.Join(root, ".eslintrc.json")) {
			return "npm run lint"
		}
	}
	switch info.Primary {
	case "Go":
		return "go vet ./..."
	case "Rust":
		return "cargo clippy"
	case "Python":
		if isFile(filepath.Join(root, "setup.cfg")) || isFile(filepath.Join(root, ".flake8")) {
			return "python -m flake8"
		}
	}
	return ""
}

func countCode(root string) (files, lines int) {
	extToCode := map[string]bool{}
	for _, l := range languages {
		for _, e := range l.Extensions {
			extToCode[e] = true
		}
	}
	_ = Walk(root, func(path string, info os.FileInfo) bool {
		if !extToCode[strings.ToLower(filepath.Ext(path))] {
			return true
		}
		files++
		data, err := os.ReadFile(path)
		if err != nil {
			return true
		}
		lines += strings.Count(string(data), "\n") + 1
		return true
	})
	return files, lines
}

// Describe renders the project as the compact text block injected into the
// system prompt.
func (i *Info) Describe() string {
	var b strings.Builder
	b.WriteString("Project: " + i.Root + "\n")
	if i.Primary != "" {
		langs := i.Primary
		if len(i.Languages) > 1 {
			langs += " (also: " + strings.Join(i.Languages[1:], ", ") + ")"
		}
		b.WriteString("Language: " + langs + "\n")
		b.WriteString(sprintf("Source files: %d, lines: %d\n", i.FileCount, i.LineCount))
	}
	if i.PackageManager != nil {
		pm := i.PackageManager
		b.WriteString(sprintf("Package manager: %s", pm.Name))
		if pm.Lockfile != "" {
			b.WriteString(" (lockfile: " + pm.Lockfile + ")")
		}
		b.WriteString("\n")
	}
	if i.BuildCommand != "" {
		b.WriteString("Build: " + i.BuildCommand + "\n")
	}
	if i.TestCommand != "" {
		b.WriteString("Tests: " + i.TestCommand + "\n")
	}
	if i.LintCommand != "" {
		b.WriteString("Lint: " + i.LintCommand + "\n")
	}
	if len(i.Frameworks) > 0 {
		b.WriteString("Libraries/frameworks: " + strings.Join(i.Frameworks, ", ") + "\n")
	}
	if len(i.Manifests) > 0 {
		b.WriteString("Manifests: " + strings.Join(i.Manifests, ", ") + "\n")
	}
	if len(i.SourceDirs) > 0 {
		b.WriteString("Source directories: " + strings.Join(i.SourceDirs, ", ") + "\n")
	}
	if len(i.TestDirs) > 0 {
		b.WriteString("Test directories: " + strings.Join(i.TestDirs, ", ") + "\n")
	}
	if len(i.EntryPoints) > 0 {
		b.WriteString("Entry points: " + strings.Join(i.EntryPoints, ", ") + "\n")
	}
	if i.ReadmePath != "" {
		b.WriteString("README: " + i.ReadmePath + "\n")
	}
	if len(i.DocDirs) > 0 {
		b.WriteString("Docs: " + strings.Join(i.DocDirs, ", ") + "\n")
	}
	if i.HasGit {
		gitState := "git: branch " + i.GitBranch
		if i.ModifiedFiles > 0 || i.UntrackedFiles > 0 {
			gitState += sprintf(" (%d modified, %d untracked)", i.ModifiedFiles, i.UntrackedFiles)
		}
		b.WriteString(gitState + "\n")
	}
	for _, w := range i.Warnings {
		b.WriteString("Warning: " + w + "\n")
	}
	return b.String()
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// sprintf is a thin wrapper kept so the description builder reads cleanly.
func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
