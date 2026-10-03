// Package index indexes project files so the agent can search and gather
// context efficiently even in large repositories.
package index

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/talon-cli/talon/internal/project"
)

// Entry describes one indexed file.
type Entry struct {
	// Path is workspace-relative and slash-separated.
	Path string
	Size int64
	// Language is the detected language name.
	Language string
	// IsTest marks test files.
	IsTest bool
	// ModTime is used to detect staleness.
	ModTime time.Time
	// Symbols holds the names found by the lightweight scanner.
	Symbols []string
}

// Index is an in-memory view of the project, rebuilt on demand.
type Index struct {
	root string

	mu      sync.RWMutex
	entries map[string]Entry
	// builtAt is when the index was last built.
	builtAt time.Time
	scanned int
}

// New creates an index for root.
func New(root string) *Index {
	return &Index{root: root, entries: map[string]Entry{}}
}

// Root returns the project root.
func (ix *Index) Root() string { return ix.root }

// Build walks the project and (re)builds the index. It is incremental: files
// whose modification time has not changed keep their symbols.
func (ix *Index) Build(ctx context.Context) error {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	fresh := make(map[string]Entry, len(ix.entries))
	scanned := 0
	err := project.Walk(ix.root, func(path string, info os.FileInfo) bool {
		select {
		case <-ctx.Done():
			return false
		default:
		}
		scanned++
		rel := relPath(ix.root, path)
		prev, hadPrev := ix.entries[rel]
		e := Entry{
			Path:     rel,
			Size:     info.Size(),
			Language: project.LanguageOf(rel),
			IsTest:   looksLikeTest(rel),
			ModTime:  info.ModTime(),
		}
		if !hadPrev || prev.ModTime.Before(info.ModTime()) || prev.Size != info.Size() {
			e.Symbols = scanSymbols(path, e.Language)
		} else {
			e.Symbols = prev.Symbols
		}
		fresh[rel] = e
		return true
	})
	if err != nil {
		return err
	}
	ix.entries = fresh
	ix.builtAt = time.Now()
	ix.scanned = scanned
	return nil
}

// Entries returns the indexed files sorted by path.
func (ix *Index) Entries() []Entry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	out := make([]Entry, 0, len(ix.entries))
	for _, e := range ix.entries {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// Len returns the number of indexed files.
func (ix *Index) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.entries)
}

// Stats describes the index contents.
type Stats struct {
	Files     int
	Languages map[string]int
	Tests     int
	Symbols   int
	Bytes     int64
	BuiltAt   time.Time
}

// Stats computes index statistics.
func (ix *Index) Stats() Stats {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	s := Stats{Languages: map[string]int{}, BuiltAt: ix.builtAt}
	for _, e := range ix.entries {
		s.Files++
		s.Bytes += e.Size
		s.Symbols += len(e.Symbols)
		if e.IsTest {
			s.Tests++
		}
		if e.Language != "" {
			s.Languages[e.Language]++
		}
	}
	return s
}

// MaxSize is the largest file the indexer will read for symbols.
const MaxSize = 1024 * 1024

func scanSymbols(path, language string) []string {
	info, err := os.Stat(path)
	if err != nil || info.Size() > MaxSize {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	pattern := symbolPattern(language)
	seen := map[string]bool{}
	var out []string
	for sc.Scan() {
		line := sc.Text()
		if len(strings.TrimSpace(line)) > 200 {
			continue
		}
		for _, m := range pattern.FindAllStringSubmatch(line, -1) {
			name := m[1]
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, name)
			if len(out) >= 200 {
				return out
			}
		}
	}
	return out
}

// genericSymbols recognises declarations across most languages; Go needs its own
// expression because methods have a receiver in between.
var genericSymbols = regexp.MustCompile(`^\s*(?:export\s+|pub\s+|public\s+|static\s+|final\s+|async\s+|private\s+|protected\s+|internal\s+)*(?:const|let|var|val|class|struct|enum|interface|type|fn|func|def|function|module|impl|trait|record|package|namespace)\s+([A-Za-z_][A-Za-z0-9_]*)`)

var goSymbols = regexp.MustCompile(`^\s*(?:func\s+(?:\([^)]*\)\s*)?|type\s+)([A-Za-z_][A-Za-z0-9_]*)`)

func symbolPattern(lang string) *regexp.Regexp {
	switch project.NormalizeLang(lang) {
	case "go":
		return goSymbols
	default:
		return genericSymbols
	}
}

// Match is one search result.
type Match struct {
	Path string
	Line int
	Text string
}

// SearchOptions controls a text search.
type SearchOptions struct {
	// Query is a plain substring or a regular expression when Regex is true.
	Query string
	Regex bool
	// CaseSensitive defaults to false.
	CaseSensitive bool
	// Glob restricts the search to paths matching this pattern.
	Glob string
	// MaxResults caps the number of matches.
	MaxResults int
	// MaxFileBytes skips files larger than this.
	MaxFileBytes int64
	// IncludeTests includes test files (true by default).
	IncludeTests bool
}

// DefaultMaxResults bounds an unqualified search.
const DefaultMaxResults = 200

// Search finds text matches across the project.
func (ix *Index) Search(ctx context.Context, opts SearchOptions) ([]Match, error) {
	if opts.Query == "" {
		return nil, fmt.Errorf("search query must not be empty")
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = DefaultMaxResults
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = 2 * 1024 * 1024
	}
	var re *regexp.Regexp
	var err error
	if opts.Regex {
		expr := opts.Query
		if !opts.CaseSensitive {
			expr = "(?i)" + expr
		}
		re, err = regexp.Compile(expr)
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression: %w", err)
		}
	}
	needle := opts.Query
	if !opts.CaseSensitive {
		needle = strings.ToLower(needle)
	}

	var matches []Match
	for _, e := range ix.Entries() {
		select {
		case <-ctx.Done():
			return matches, ctx.Err()
		default:
		}
		if !opts.IncludeTests && e.IsTest {
			continue
		}
		if opts.Glob != "" {
			if ok, gerr := filepath.Match(opts.Glob, e.Path); gerr != nil || !ok {
				continue
			}
		}
		if e.Size > opts.MaxFileBytes {
			continue
		}
		path := filepath.Join(ix.root, filepath.FromSlash(e.Path))
		content, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		body := string(content)
		lines := strings.Split(body, "\n")
		for i, line := range lines {
			hit := false
			if re != nil {
				hit = re.MatchString(line)
			} else if opts.CaseSensitive {
				hit = strings.Contains(line, opts.Query)
			} else {
				hit = strings.Contains(strings.ToLower(line), needle)
			}
			if !hit {
				continue
			}
			matches = append(matches, Match{Path: e.Path, Line: i + 1, Text: strings.TrimRight(line, "\r")})
			if len(matches) >= opts.MaxResults {
				return matches, nil
			}
		}
	}
	return matches, nil
}

// FindFiles returns files whose path matches the glob, sorted.
func (ix *Index) FindFiles(glob string, limit int) []string {
	var out []string
	for _, e := range ix.Entries() {
		ok := false
		if m, err := filepath.Match(glob, e.Path); err == nil && m {
			ok = true
		} else if m, err := filepath.Match(glob, filepath.Base(e.Path)); err == nil && m {
			ok = true
		} else if !strings.ContainsAny(glob, "*?[") && strings.Contains(e.Path, glob) {
			ok = true
		}
		if ok {
			out = append(out, e.Path)
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out
}

// FindSymbols returns symbols whose name matches, with their file.
func (ix *Index) FindSymbols(name string, limit int) []Entry {
	var out []Entry
	lower := strings.ToLower(name)
	for _, e := range ix.Entries() {
		for _, s := range e.Symbols {
			if strings.EqualFold(s, name) || strings.Contains(strings.ToLower(s), lower) {
				out = append(out, e)
				break
			}
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// Related returns files that frequently appear with path, based on shared
// directory and on imports mentioned in path.
func (ix *Index) Related(path string, limit int) []string {
	base := filepath.Base(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	ext := strings.TrimPrefix(filepath.Ext(base), ".")
	dir := filepath.Dir(path)
	seen := map[string]bool{path: true}
	var out []string
	// Siblings first, then files that mention the base name.
	for _, e := range ix.Entries() {
		if limit > 0 && len(out) >= limit {
			break
		}
		if seen[e.Path] || filepath.Dir(e.Path) != dir {
			continue
		}
		if strings.TrimPrefix(filepath.Ext(e.Path), ".") == ext {
			out = append(out, e.Path)
			seen[e.Path] = true
		}
	}
	for _, e := range ix.Entries() {
		if limit > 0 && len(out) >= limit {
			break
		}
		if seen[e.Path] || e.IsTest {
			continue
		}
		data, err := os.ReadFile(filepath.Join(ix.root, filepath.FromSlash(e.Path)))
		if err != nil || len(data) > MaxSize {
			continue
		}
		body := string(data)
		if strings.Contains(body, base) || (stem != "" && strings.Contains(body, stem)) {
			out = append(out, e.Path)
			seen[e.Path] = true
		}
	}
	return out
}

func looksLikeTest(rel string) bool {
	lower := strings.ToLower(rel)
	base := filepath.Base(lower)
	switch {
	case strings.Contains(lower, "test") || strings.Contains(lower, "spec"):
		return true
	case strings.HasSuffix(base, "_test.go"):
		return true
	case strings.HasPrefix(base, "test_"):
		return true
	}
	return false
}

func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}
