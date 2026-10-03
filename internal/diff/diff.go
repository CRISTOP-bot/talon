// Package diff computes line-based unified diffs. It is used by the edit
// tools, the diff viewer and the rollback journal.
package diff

import (
	"fmt"
	"strings"
)

// Op is the kind of change on a line.
type Op int

// Line operations.
const (
	OpEqual Op = iota
	OpAdd
	OpDelete
)

// Line is one line of a diff.
type Line struct {
	Op   Op
	Text string
	// OldNo and NewNo are 1-based line numbers; 0 when not applicable.
	OldNo int
	NewNo int
}

// Hunk is a contiguous group of changes.
type Hunk struct {
	OldStart, OldCount int
	NewStart, NewCount int
	Lines              []Line
	// Header is the text that precedes the hunk body, e.g. "@@ func main @@".
	Header string
}

// Stats counts additions and deletions.
type Stats struct {
	Added   int
	Deleted int
}

// Diff computes a unified diff between two texts.
func Diff(old, updated string, contextLines int) ([]Hunk, Stats) {
	a := splitLines(old)
	b := splitLines(updated)
	ops := diffLines(a, b)

	var hunks []Hunk
	var stats Stats
	for _, op := range ops {
		switch op.op {
		case OpAdd:
			stats.Added++
		case OpDelete:
			stats.Deleted++
		}
	}

	// Group changed regions into hunks with context.
	idx := 0
	for idx < len(ops) {
		if ops[idx].op == OpEqual {
			idx++
			continue
		}
		start := idx - contextLines
		if start < 0 {
			start = 0
		}
		end := idx
		for end < len(ops) {
			if ops[end].op != OpEqual {
				end++
				continue
			}
			// Count consecutive equal lines; stop when the run ends.
			run := 0
			for end+run < len(ops) && ops[end+run].op == OpEqual {
				run++
			}
			if run > 2*contextLines || end+run >= len(ops) {
				break
			}
			end += run
		}
		stop := end + contextLines
		if stop > len(ops) {
			stop = len(ops)
		}
		h := buildHunk(ops[start:stop], sectionContext(a, ops[start].oldNo))
		hunks = append(hunks, h)
		idx = stop
		if idx <= start {
			idx = start + 1
		}
	}
	return hunks, stats
}

func buildHunk(ops []opLine, context string) Hunk {
	h := Hunk{Header: context}
	for _, o := range ops {
		h.Lines = append(h.Lines, Line{Op: o.op, Text: o.text, OldNo: o.oldNo, NewNo: o.newNo})
		if o.oldNo > 0 && h.OldStart == 0 {
			h.OldStart = o.oldNo
		}
		if o.newNo > 0 && h.NewStart == 0 {
			h.NewStart = o.newNo
		}
		if o.op != OpAdd {
			h.OldCount++
		}
		if o.op != OpDelete {
			h.NewCount++
		}
	}
	return h
}

// sectionPrefixes start the lines used as the "@@ context @@" suffix.
var sectionPrefixes = []string{
	"func ", "fn ", "pub fn ", "def ", "class ", "struct ", "enum ", "impl ", "trait ",
	"interface ", "type ", "module ", "namespace ", "package ", "public class ", "const ",
	"var ", "let ", "function ", "#", "//", "/*", "export ",
}

// sectionContext finds the nearest enclosing declaration above lineNo (1-based)
// so hunk headers carry a familiar context suffix.
func sectionContext(lines []string, lineNo int) string {
	start := lineNo - 1
	if start > len(lines) {
		start = len(lines)
	}
	limit := start - 200
	if limit < 0 {
		limit = 0
	}
	for i := start - 1; i >= limit; i-- {
		l := strings.TrimSpace(lines[i])
		for _, p := range sectionPrefixes {
			if strings.HasPrefix(l, p) {
				if len(l) > 60 {
					l = l[:60]
				}
				return " " + l
			}
		}
	}
	return ""
}

type opLine struct {
	op           Op
	text         string
	oldNo, newNo int
}

// diffLines produces an edit script using the classic LCS dynamic program. The
// inputs are trimmed to a bounded size so pathological inputs cannot stall the
// agent.
func diffLines(a, b []string) []opLine {
	maxCells := 4_000_000
	if len(a)*len(b) > maxCells {
		return coarseDiff(a, b)
	}
	n, m := len(a), len(b)
	// lcs[i][j] = length of the longest common subsequence of a[i:] and b[j:]
	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var ops []opLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, opLine{op: OpEqual, text: a[i], oldNo: i + 1, newNo: j + 1})
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			ops = append(ops, opLine{op: OpDelete, text: a[i], oldNo: i + 1})
			i++
		default:
			ops = append(ops, opLine{op: OpAdd, text: b[j], newNo: j + 1})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, opLine{op: OpDelete, text: a[i], oldNo: i + 1})
	}
	for ; j < m; j++ {
		ops = append(ops, opLine{op: OpAdd, text: b[j], newNo: j + 1})
	}
	return ops
}

// coarseDiff emits a delete-all/insert-all diff for very large inputs.
func coarseDiff(a, b []string) []opLine {
	var ops []opLine
	for i, l := range a {
		ops = append(ops, opLine{op: OpDelete, text: l, oldNo: i + 1})
	}
	for j, l := range b {
		ops = append(ops, opLine{op: OpAdd, text: l, newNo: j + 1})
	}
	return ops
}

// splitLines splits text into lines, dropping a single trailing newline so
// that a missing final newline does not show up as a change.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// Unified renders a standard unified diff with a header, used by `git diff`-like
// output and by the apply preview.
func Unified(oldName, newName, old, updated string, contextLines int) string {
	hunks, stats := Diff(old, updated, contextLines)
	var b strings.Builder
	fmt.Fprintf(&b, "--- a/%s\n+++ b/%s\n", oldName, newName)
	for _, h := range hunks {
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@%s\n", h.OldStart, h.OldCount, h.NewStart, h.NewCount, h.Header)
		for _, l := range h.Lines {
			b.WriteString(lineText(l))
		}
	}
	if len(hunks) == 0 {
		fmt.Fprintf(&b, " (no changes: %d added, %d deleted)\n", stats.Added, stats.Deleted)
	}
	return b.String()
}

func lineText(l Line) string {
	switch l.Op {
	case OpAdd:
		return "+" + l.Text + "\n"
	case OpDelete:
		return "-" + l.Text + "\n"
	default:
		return " " + l.Text + "\n"
	}
}

// LineText renders a single diff line in unified format (exported for the UI).
func LineText(l Line) string { return lineText(l) }

// Preview renders a compact per-file summary of what a patch would do.
func Preview(stats Stats) string {
	if stats.Added == 0 && stats.Deleted == 0 {
		return "no changes"
	}
	return fmt.Sprintf("+%d −%d", stats.Added, stats.Deleted)
}

// FileDiff bundles the changes to one file, which is what the UI renders and
// the tools return.
type FileDiff struct {
	Path string
	// OldPath differs from Path for renames.
	OldPath  string
	Hunks    []Hunk
	Stats    Stats
	Binarily bool
}

// Empty reports whether the diff carries no changes.
func (f FileDiff) Empty() bool { return len(f.Hunks) == 0 }
