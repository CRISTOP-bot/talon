// Package privacy implements privacy mode and the inventory of data Talon
// keeps, so the user can see and delete it.
//
// Privacy mode is an explicit, visible trade-off: it stops Talon writing
// anything durable, and it says plainly what it cannot do — namely stop a
// model provider from receiving the prompt you sent it.
package privacy

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/paths"
)

// Mode describes an active privacy configuration.
type Mode struct {
	Enabled bool
	// Reason is recorded so the audit log can explain why persistence is off.
	Reason string
}

// Limitations are the things privacy mode cannot prevent. They are shown to the
// user verbatim, because a privacy mode that overstates itself is worse than no
// privacy mode.
func Limitations() []string {
	return []string{
		"Prompts and any file content the model needs are still sent to the provider you configured; " +
			"run a local provider (ollama, llamacpp) if that must not happen.",
		"The provider's own logging and retention apply to what it receives; Talon cannot control that.",
		"Your shell, package managers and the tools Talon invokes keep their own histories and caches.",
		"The OS may keep shell scrollback, terminal logs or crash reports outside Talon's control.",
	}
}

// Notice renders the privacy-mode banner, including its limits.
func (m Mode) Notice() string {
	if !m.Enabled {
		return ""
	}
	var b strings.Builder
	b.WriteString("privacy mode is ON\n")
	if m.Reason != "" {
		fmt.Fprintf(&b, "  reason: %s\n", m.Reason)
	}
	b.WriteString("  disabled: session persistence, memory writes, debug logs, audit log, undo journal\n")
	b.WriteString("  still true: prompts are sent to your configured model provider\n")
	b.WriteString("  limitations:\n")
	for _, l := range Limitations() {
		fmt.Fprintf(&b, "    - %s\n", l)
	}
	return b.String()
}

// Category groups related data.
type Category string

// Data categories.
const (
	CatSessions Category = "sessions"
	CatMemory   Category = "memory"
	CatLogs     Category = "logs"
	CatHistory  Category = "history"
	CatAudit    Category = "audit"
	CatJournal  Category = "journal"
	CatIndex    Category = "index"
	CatLegal    Category = "legal"
	CatPlugins  Category = "plugins"
	CatProject  Category = "project"
)

// labels are the human names shown in `talon data list`.
var labels = map[Category]string{
	CatSessions: "conversation transcripts",
	CatMemory:   "memory notes",
	CatLogs:     "debug logs",
	CatHistory:  "input history",
	CatAudit:    "security audit log",
	CatJournal:  "undo journal",
	CatIndex:    "project index cache",
	CatLegal:    "terms and privacy consent records",
	CatPlugins:  "installed plugins",
	CatProject:  "project-local .talon directory",
}

// Label returns the human name of a category.
func (c Category) Label() string {
	if l, ok := labels[c]; ok {
		return l
	}
	return string(c)
}

// Item is one stored artefact.
type Item struct {
	Category Category
	Path     string
	Exists   bool
	Files    int
	Bytes    int64
	// Scope is "user" or "project".
	Scope string
	// Project is set for project-scoped items.
	Project string
}

// Human renders the size for the CLI.
func (i Item) Human() string {
	if !i.Exists {
		return "absent"
	}
	switch {
	case i.Bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB in %d file(s)", float64(i.Bytes)/(1<<20), i.Files)
	case i.Bytes >= 1<<20/1024:
		return fmt.Sprintf("%.1f KB in %d file(s)", float64(i.Bytes)/1024, i.Files)
	default:
		return fmt.Sprintf("%d byte(s) in %d file(s)", i.Bytes, i.Files)
	}
}

// Inventory lists everything Talon may have stored, for `talon data list`.
func Inventory(projectRoot string) []Item {
	items := []Item{
		{Category: CatSessions, Path: paths.SessionsDir(), Scope: "user"},
		{Category: CatMemory, Path: paths.MemoryDir(), Scope: "user"},
		{Category: CatLogs, Path: paths.LogDir(), Scope: "user"},
		{Category: CatHistory, Path: paths.HistoryFile(), Scope: "user"},
		{Category: CatAudit, Path: AuditPath(), Scope: "user"},
		{Category: CatJournal, Path: filepath.Join(paths.Cache(), "journal"), Scope: "user"},
		{Category: CatIndex, Path: filepath.Join(paths.Cache(), "index"), Scope: "user"},
		{Category: CatLegal, Path: LegalPath(), Scope: "user"},
		{Category: CatPlugins, Path: paths.PluginDir(), Scope: "user"},
	}
	if projectRoot != "" {
		items = append(items, Item{
			Category: CatProject,
			Path:     filepath.Join(projectRoot, ".talon"),
			Scope:    "project",
			Project:  projectRoot,
		})
	}
	for i := range items {
		files, bytes := measure(items[i].Path)
		items[i].Files, items[i].Bytes = files, bytes
		items[i].Exists = files > 0
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Category < items[j].Category })
	return items
}

// AuditPath returns the audit log path (kept here so privacy and audit agree).
func AuditPath() string { return filepath.Join(paths.Data(), "audit", "audit.jsonl") }

// LegalPath returns the consent ledger path.
func LegalPath() string { return filepath.Join(paths.Data(), "legal", "legal.json") }

// measure counts files and bytes under a path, following the same rules Talon
// applies when it writes them.
func measure(path string) (int, int64) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, 0
	}
	if !info.IsDir() {
		return 1, info.Size()
	}
	var files int
	var total int64
	_ = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		fi, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		files++
		total += fi.Size()
		return nil
	})
	return files, total
}

// Total returns the number of files and bytes across the inventory.
func Total(items []Item) (int, int64) {
	var files int
	var bytes int64
	for _, i := range items {
		files += i.Files
		bytes += i.Bytes
	}
	return files, bytes
}

// ClearRequest names what the user asked to delete.
type ClearRequest struct {
	// Categories restricts deletion; empty means everything user-scoped.
	Categories []Category
	// ProjectRoot enables deleting the project-local .talon directory.
	ProjectRoot string
	// IncludePlugins also removes installed plugins.
	IncludePlugins bool
}

// Plan describes what a deletion would remove, so it can be confirmed before
// anything is deleted.
type Plan struct {
	Items []Item
	// Missing lists paths that do not exist, which are reported rather than
	// silently ignored.
	Missing []string
}

// Bytes returns the total size a plan would delete.
func (p Plan) Bytes() int64 {
	var total int64
	for _, i := range p.Items {
		total += i.Bytes
	}
	return total
}

// BuildPlan resolves a deletion request against the current inventory.
func BuildPlan(req ClearRequest) Plan {
	want := map[Category]bool{}
	for _, c := range req.Categories {
		want[c] = true
	}
	plan := Plan{}
	for _, item := range Inventory(req.ProjectRoot) {
		if !item.Exists {
			plan.Missing = append(plan.Missing, item.Path)
			continue
		}
		include := len(want) == 0
		if len(want) > 0 {
			include = want[item.Category]
		}
		if !include {
			continue
		}
		if item.Category == CatPlugins && !req.IncludePlugins {
			continue
		}
		if item.Scope == "project" && req.ProjectRoot == "" {
			continue
		}
		plan.Items = append(plan.Items, item)
	}
	return plan
}

// Apply performs a planned deletion.
func Apply(plan Plan) error {
	for _, item := range plan.Items {
		if err := os.RemoveAll(item.Path); err != nil {
			return fmt.Errorf("cannot delete %s: %w", item.Path, err)
		}
	}
	return nil
}

// Categories returns the deletable category names, sorted.
func Categories() []Category {
	all := []Category{
		CatSessions, CatMemory, CatLogs, CatHistory, CatAudit, CatJournal,
		CatIndex, CatLegal, CatPlugins, CatProject,
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	return all
}

// ParseCategory converts user input into a category, rejecting unknown names.
func ParseCategory(s string) (Category, error) {
	c := Category(strings.ToLower(strings.TrimSpace(s)))
	for _, known := range Categories() {
		if known == c {
			return c, nil
		}
	}
	return "", fmt.Errorf("unknown data category %q (known: %v)", s, Categories())
}
