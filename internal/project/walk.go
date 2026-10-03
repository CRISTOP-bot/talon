package project

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// DefaultIgnoredDirs are never walked: they are generated, vendored or huge.
var DefaultIgnoredDirs = map[string]bool{
	".git": true, "node_modules": true, "vendor": true, "target": true,
	"dist": true, "build": true, "out": true, ".next": true, ".nuxt": true,
	"__pycache__": true, ".pytest_cache": true, ".mypy_cache": true, ".ruff_cache": true,
	".venv": true, "venv": true, "env": true, ".env.local": true,
	".gradle": true, ".idea": true, ".vscode": true, ".cache": true, ".parcel-cache": true,
	"coverage": true, ".nyc_output": true, ".terraform": true, ".serverless": true,
	"Pods": true, "DerivedData": true, ".dart_tool": true, ".talon": true,
	"deps": true, "_build": true, ".stack-work": true, "zig-out": true, "zig-cache": true,
	".tox": true, ".eggs": true, "bin": true, "obj": true, "cmake-build-debug": true,
}

// DefaultIgnoredExts are skipped by the index and the scanner.
var DefaultIgnoredExts = map[string]bool{
	".lock": false, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".ico": true, ".svg": true, ".pdf": true, ".zip": true,
	".gz": true, ".tar": true, ".bz2": true, ".xz": true, ".7z": true, ".rar": true,
	".mp3": true, ".mp4": true, ".wav": true, ".mov": true, ".avi": true, ".webm": true,
	".so": true, ".dylib": true, ".dll": true, ".a": true, ".o": true, ".class": true,
	".jar": true, ".war": true, ".exe": true, ".bin": true, ".pyc": true, ".pyo": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".db": true, ".sqlite": true, ".sqlite3": true, ".wasm": true, ".onnx": true,
	".pt": true, ".pth": true, ".safetensors": true, ".pack": true, ".idx": true,
	".talon": true,
}

// Walk visits every non-ignored file under root. The callback returns false to
// stop descending. It respects .gitignore files and DefaultIgnoredDirs.
func Walk(root string, fn func(path string, info os.FileInfo) bool) error {
	patterns, err := loadGitignore(root)
	if err != nil {
		patterns = nil
	}
	visited := 0
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Unreadable entries are skipped rather than aborting the walk.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if name != root && DefaultIgnoredDirs[name] {
				return fs.SkipDir
			}
			if name == ".git" {
				return fs.SkipDir
			}
			if matchIgnore(patterns, rel, true) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if DefaultIgnoredExts[strings.ToLower(filepath.Ext(name))] {
			return nil
		}
		if matchIgnore(patterns, rel, false) {
			return nil
		}
		visited++
		if visited > maxScanFiles*4 {
			return filepath.SkipAll
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		if !fn(path, info) {
			return fs.SkipDir
		}
		return nil
	})
}

// loadGitignore reads the .gitignore files from the root down, so nested rules
// are honoured.
func loadGitignore(root string) ([]string, error) {
	var patterns []string
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns, nil
}

// matchIgnore reports whether a path matches a gitignore-style pattern.
func matchIgnore(patterns []string, rel string, isDir bool) bool {
	if len(patterns) == 0 {
		return false
	}
	rel = filepath.ToSlash(rel)
	base := baseName(rel)
	matched := false
	for _, p := range patterns {
		negate := strings.HasPrefix(p, "!")
		if negate {
			p = p[1:]
		}
		p = strings.TrimSuffix(p, "/")
		if p == "" {
			continue
		}
		hit := false
		switch {
		case strings.Contains(p, "/"):
			if ok, err := filepath.Match(p, rel); err == nil && ok {
				hit = true
			}
			if !hit && strings.HasPrefix(rel, strings.TrimSuffix(p, "/")+"/") {
				hit = true
			}
		default:
			if ok, err := filepath.Match(p, base); err == nil && ok {
				hit = true
			}
		}
		if hit {
			if negate {
				return false
			}
			matched = true
		}
	}
	return matched && (isDir || true)
}

// baseName returns the last path segment of a slash-separated path.
func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// SortPaths orders paths for stable output.
func SortPaths(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		di := strings.Count(paths[i], "/")
		dj := strings.Count(paths[j], "/")
		if di != dj {
			return di < dj
		}
		return paths[i] < paths[j]
	})
}

// LanguageOf maps a file extension to a language name.
func LanguageOf(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	for _, l := range languages {
		for _, e := range l.Extensions {
			if e == ext {
				return l.Name
			}
		}
	}
	return ""
}

var langAliases = map[string]string{
	"js": "javascript", "ts": "typescript", "py": "python", "rs": "rust",
	"sh": "shell", "bash": "shell", "zsh": "shell", "cpp": "cpp", "c++": "cpp",
	"cs": "csharp", "kt": "kotlin", "yml": "yaml", "rb": "ruby", "ex": "elixir",
	"exs": "elixir", "golang": "go", "objc": "objective-c",
}

// NormalizeLang maps a language alias to its canonical name.
func NormalizeLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	if canonical, ok := langAliases[l]; ok {
		return canonical
	}
	return l
}

// binAvailable reports whether a binary is on PATH.
func binAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// varPattern matches simple declarations so the indexer can find symbols
// without a full parser per language.
var varPattern = regexp.MustCompile(`(?m)^(?:export\s+|pub\s+|public\s+|static\s+|final\s+)*(?:const|let|var|val|class|struct|enum|interface|type|fn|func|def|function|module|impl|trait|record)\s+([A-Za-z_][A-Za-z0-9_]*)`)
