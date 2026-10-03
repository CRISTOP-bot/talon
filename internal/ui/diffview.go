package ui

import (
	"fmt"
	"io"
	"strings"

	"github.com/talon-cli/talon/internal/diff"
)

// RenderDiff writes a coloured unified diff, grouped by file when a path is
// present.
func (p *Printer) RenderDiff(w io.Writer, files []diff.FileDiff, contextLines int) {
	t := p.Theme
	for i, f := range files {
		if i > 0 {
			fmt.Fprintln(w)
		}
		name := f.Path
		if f.OldPath != "" && f.OldPath != f.Path {
			name = f.OldPath + " → " + f.Path
		}
		header := fmt.Sprintf("%s  %s", name, PreviewStats(f.Stats))
		fmt.Fprintln(w, t.Strong(header))
		fmt.Fprintln(w, t.BorderLine(strings.Repeat("─", minInt(len([]rune(header))+2, 78))))
		if f.Binarily {
			fmt.Fprintln(w, t.Faint("  binary file differs"))
			continue
		}
		if len(f.Hunks) == 0 {
			fmt.Fprintln(w, t.Faint("  no changes"))
			continue
		}
		for _, h := range f.Hunks {
			fmt.Fprintf(w, "  %s\n", t.Paint(t.DiffHunk, diffHeader(h)))
			for _, l := range h.Lines {
				fmt.Fprintln(w, renderDiffLine(l, t))
			}
		}
	}
}

func diffHeader(h diff.Hunk) string {
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@%s", h.OldStart, h.OldCount, h.NewStart, h.NewCount, h.Header)
}

func renderDiffLine(l diff.Line, t Theme) string {
	text := l.Text
	// Expand tabs so columns line up in the terminal.
	text = strings.ReplaceAll(text, "\t", "    ")
	switch l.Op {
	case diff.OpAdd:
		return t.Paint(t.DiffAdd, "+ "+text)
	case diff.OpDelete:
		return t.Paint(t.DiffDel, "- "+text)
	default:
		return t.Faint("  " + text)
	}
}

// PreviewStats renders "+3 −1".
func PreviewStats(s diff.Stats) string { return diff.Preview(s) }

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// DiffFromText builds a FileDiff from two texts (used by tools and /undo).
func DiffFromText(path, old, updated string, contextLines int) diff.FileDiff {
	hunks, stats := diff.Diff(old, updated, contextLines)
	return diff.FileDiff{Path: path, Hunks: hunks, Stats: stats}
}
