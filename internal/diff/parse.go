package diff

import (
	"fmt"
	"strconv"
	"strings"
)

// ParseUnified reads unified diff text (as produced by `git diff`) and returns
// the per-file diffs it describes.
func ParseUnified(text string) []FileDiff {
	var files []FileDiff
	var current *FileDiff
	hunkIndex := -1
	// Remaining lines declared by the current hunk header. Unified diffs do not
	// carry a terminator, so counts are the only way to know when a hunk ends.
	oldRemain, newRemain := 0, 0

	ensureFile := func() {
		if current == nil {
			files = append(files, FileDiff{})
			current = &files[len(files)-1]
		}
	}

	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			current = nil
			ensureFile()
			current.Path = parseGitHeaderPath(line)
		case strings.HasPrefix(line, "--- "), strings.HasPrefix(line, "+++ "):
			if current == nil {
				continue
			}
			path := strings.TrimSpace(line[4:])
			path = strings.TrimPrefix(path, "a/")
			path = strings.TrimPrefix(path, "b/")
			if path != "/dev/null" {
				current.Path = path
			}
		case strings.HasPrefix(line, "@@"):
			ensureFile()
			h := parseHunkHeader(line)
			current.Hunks = append(current.Hunks, h)
			hunkIndex = len(current.Hunks) - 1
			oldRemain, newRemain = h.OldCount, h.NewCount
		case hunkIndex >= 0 && current != nil && strings.HasPrefix(line, "+") && newRemain > 0:
			h := &current.Hunks[hunkIndex]
			h.Lines = append(h.Lines, Line{Op: OpAdd, Text: line[1:]})
			newRemain--
		case hunkIndex >= 0 && current != nil && strings.HasPrefix(line, "-") && oldRemain > 0:
			h := &current.Hunks[hunkIndex]
			h.Lines = append(h.Lines, Line{Op: OpDelete, Text: line[1:]})
			oldRemain--
		case hunkIndex >= 0 && current != nil && (strings.HasPrefix(line, " ") || line == "") && oldRemain > 0 && newRemain > 0:
			// A blank line inside a hunk is context, not a new statement.
			h := &current.Hunks[hunkIndex]
			text := ""
			if strings.HasPrefix(line, " ") {
				text = line[1:]
			}
			h.Lines = append(h.Lines, Line{Op: OpEqual, Text: text})
			oldRemain--
			newRemain--
		}
	}

	for i := range files {
		for j := range files[i].Hunks {
			h := &files[i].Hunks[j]
			h.OldCount, h.NewCount = 0, 0
			for _, l := range h.Lines {
				switch l.Op {
				case OpAdd:
					h.NewCount++
					files[i].Stats.Added++
				case OpDelete:
					h.OldCount++
					files[i].Stats.Deleted++
				default:
					h.OldCount++
					h.NewCount++
				}
			}
		}
	}
	return files
}

// Stats counts the changes in a hunk.
func (h Hunk) Stats() Stats {
	var s Stats
	for _, l := range h.Lines {
		switch l.Op {
		case OpAdd:
			s.Added++
		case OpDelete:
			s.Deleted++
		}
	}
	return s
}

func parseHunkHeader(line string) Hunk {
	// @@ -12,7 +12,9 @@ optional section heading
	h := Hunk{}
	fields := strings.Fields(line)
	if len(fields) >= 2 {
		h.OldStart, h.OldCount = parseRange(strings.TrimPrefix(fields[1], "-"))
	}
	if len(fields) >= 3 {
		h.NewStart, h.NewCount = parseRange(strings.TrimPrefix(fields[2], "+"))
	}
	if i := strings.Index(line, "@@"); i >= 0 {
		rest := line[i+2:]
		if j := strings.Index(rest, "@@"); j >= 0 {
			h.Header = strings.TrimSpace(rest[:j])
		}
	}
	return h
}

func parseRange(s string) (start, count int) {
	parts := strings.SplitN(s, ",", 2)
	start, _ = strconv.Atoi(strings.TrimSpace(parts[0]))
	if len(parts) == 2 {
		count, _ = strconv.Atoi(strings.TrimSpace(parts[1]))
	} else {
		count = 1
	}
	return start, count
}

func parseGitHeaderPath(line string) string {
	// diff --git a/path b/path
	fields := strings.Fields(line)
	if len(fields) >= 4 {
		return strings.TrimPrefix(fields[3], "b/")
	}
	return strings.TrimPrefix(line, "diff --git ")
}

// String renders a compact per-file summary, used in log lines.
func (f FileDiff) String() string {
	return fmt.Sprintf("%s %s", f.Path, Preview(f.Stats))
}
