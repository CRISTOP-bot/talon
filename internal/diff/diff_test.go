package diff

import (
	"fmt"
	"strings"
	"testing"
)

func TestDiffSimpleChange(t *testing.T) {
	old := "line one\nline two\nline three\n"
	updated := "line one\nline 2\nline three\n"
	hunks, stats := Diff(old, updated, 3)
	if len(hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(hunks))
	}
	if stats.Added != 1 || stats.Deleted != 1 {
		t.Errorf("stats = %+v", stats)
	}
	h := hunks[0]
	if h.OldCount != 3 || h.NewCount != 3 {
		t.Errorf("hunk counts = -%d,%d +%d,%d", h.OldStart, h.OldCount, h.NewStart, h.NewCount)
	}
	var adds, dels int
	for _, l := range h.Lines {
		switch l.Op {
		case OpAdd:
			adds++
			if l.Text != "line 2" {
				t.Errorf("added line = %q", l.Text)
			}
		case OpDelete:
			dels++
		}
	}
	if adds != 1 || dels != 1 {
		t.Errorf("adds=%d dels=%d", adds, dels)
	}
}

func TestDiffNoChanges(t *testing.T) {
	s := "a\nb\nc\n"
	hunks, stats := Diff(s, s, 3)
	if len(hunks) != 0 {
		t.Errorf("expected no hunks, got %d", len(hunks))
	}
	if stats.Added != 0 || stats.Deleted != 0 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestDiffAppendOnly(t *testing.T) {
	hunks, stats := Diff("a\nb\n", "a\nb\nc\n", 2)
	if stats.Added != 1 || stats.Deleted != 0 {
		t.Errorf("stats = %+v", stats)
	}
	if len(hunks) == 0 {
		t.Fatal("expected a hunk")
	}
}

func TestDiffSeparateHunks(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 0; i < 40; i++ {
		oldB.WriteString(fmt.Sprintf("line %d\n", i))
		newB.WriteString(fmt.Sprintf("line %d\n", i))
	}
	old := oldB.String()
	updated := newB.String()
	updated = strings.Replace(updated, "line 5\n", "line five\n", 1)
	updated = strings.Replace(updated, "line 30\n", "line thirty\n", 1)
	hunks, _ := Diff(old, updated, 2)
	if len(hunks) != 2 {
		t.Fatalf("hunks = %d, want 2", len(hunks))
	}
	if hunks[0].OldStart >= hunks[1].OldStart {
		t.Errorf("hunks out of order: %d then %d", hunks[0].OldStart, hunks[1].OldStart)
	}
}

func TestUnifiedFormat(t *testing.T) {
	out := Unified("main.go", "main.go", "a\nb\n", "a\nc\n", 3)
	if !strings.Contains(out, "--- a/main.go") || !strings.Contains(out, "+++ b/main.go") {
		t.Errorf("missing headers:\n%s", out)
	}
	if !strings.Contains(out, "@@ -1,2 +1,2 @@") {
		t.Errorf("missing hunk header:\n%s", out)
	}
	if !strings.Contains(out, "-b\n") || !strings.Contains(out, "+c\n") {
		t.Errorf("missing changed lines:\n%s", out)
	}
}

func TestDiffHandlesCRLFAndMissingTrailingNewline(t *testing.T) {
	a := "one\r\ntwo\r\n"
	b := "one\r\ntwo"
	hunks, stats := Diff(a, b, 2)
	if stats.Added != 0 || stats.Deleted != 0 {
		t.Errorf("crlf/trailing newline should not create changes: %+v", stats)
	}
	if len(hunks) != 0 {
		t.Errorf("hunks = %d", len(hunks))
	}
}

func TestEmptyInputs(t *testing.T) {
	if hunks, _ := Diff("", "", 3); len(hunks) != 0 {
		t.Error("empty vs empty should have no hunks")
	}
	_, stats := Diff("", "new\n", 3)
	if stats.Added != 1 {
		t.Errorf("stats = %+v", stats)
	}
	_, stats = Diff("old\n", "", 3)
	if stats.Deleted != 1 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestCoarseDiffFallbackIsStillCorrect(t *testing.T) {
	// Large inputs exercise the coarse path; correctness (not minimality) is
	// what matters there.
	var a, b []string
	for i := 0; i < 2100; i++ {
		a = append(a, fmt.Sprintf("old %d", i))
		b = append(b, fmt.Sprintf("new %d", i))
	}
	old, updated := strings.Join(a, "\n"), strings.Join(b, "\n")
	hunks, stats := Diff(old, updated, 1)
	if stats.Added != len(b) || stats.Deleted != len(a) {
		t.Errorf("stats = %+v", stats)
	}
	if len(hunks) == 0 {
		t.Error("expected hunks")
	}
}

func TestParseUnifiedFromGit(t *testing.T) {
	patch := `diff --git a/src/main.go b/src/main.go
index 1234567..89abcde 100644
--- a/src/main.go
+++ b/src/main.go
@@ -1,5 +1,6 @@
 package main
 
 func main() {
-	println("hi")
+	println("hello")
+	// added line
 }
`
	files := ParseUnified(patch)
	if len(files) != 1 {
		t.Fatalf("files = %d", len(files))
	}
	f := files[0]
	if f.Path != "src/main.go" {
		t.Errorf("path = %q", f.Path)
	}
	if f.Stats.Added != 2 || f.Stats.Deleted != 1 {
		t.Errorf("stats = %+v", f.Stats)
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("hunks = %d", len(f.Hunks))
	}
	h := f.Hunks[0]
	if h.OldStart != 1 || h.NewStart != 1 {
		t.Errorf("hunk ranges = -%d,%d +%d,%d", h.OldStart, h.OldCount, h.NewStart, h.NewCount)
	}
	if h.OldCount != 5 || h.NewCount != 6 {
		t.Errorf("hunk counts = -%d,%d +%d,%d", h.OldStart, h.OldCount, h.NewStart, h.NewCount)
	}
	var sawAdded bool
	for _, l := range h.Lines {
		if l.Op == OpAdd && strings.TrimSpace(l.Text) == "// added line" {
			sawAdded = true
		}
	}
	if !sawAdded {
		t.Errorf("lines = %+v", h.Lines)
	}
}

func TestParseUnifiedMultipleFiles(t *testing.T) {
	patch := `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1 +1 @@
-one
+two
diff --git a/b.txt b/b.txt
--- a/b.txt
+++ b/b.txt
@@ -1,2 +1,2 @@
 keep
-drop
+add
`
	files := ParseUnified(patch)
	if len(files) != 2 {
		t.Fatalf("files = %d: %+v", len(files), files)
	}
	if files[0].Path != "a.txt" || files[1].Path != "b.txt" {
		t.Errorf("paths = %q %q", files[0].Path, files[1].Path)
	}
	if files[0].Stats.Added != 1 || files[1].Stats.Deleted != 1 {
		t.Errorf("stats = %+v %+v", files[0].Stats, files[1].Stats)
	}
}

func TestParseUnifiedHandlesSectionHeaders(t *testing.T) {
	patch := `@@ -10,3 +10,4 @@ func main() {
 	context
-old
+new
+extra
`
	files := ParseUnified(patch)
	if len(files) != 1 {
		t.Fatalf("files = %d", len(files))
	}
	h := files[0].Hunks[0]
	if h.OldStart != 10 || h.NewStart != 10 {
		t.Errorf("ranges = -%d +%d", h.OldStart, h.NewStart)
	}
	if h.OldCount != 3 || h.NewCount != 4 {
		t.Errorf("counts = %d/%d", h.OldCount, h.NewCount)
	}
}

func TestFileDiffHelpers(t *testing.T) {
	fd := FileDiff{Path: "a.go"}
	if !fd.Empty() {
		t.Error("a fresh FileDiff should be empty")
	}
	fd.Hunks, fd.Stats = Diff("a\n", "b\n", 1)
	if fd.Empty() {
		t.Error("FileDiff with hunks should not be empty")
	}
	if got := fd.String(); !strings.Contains(got, "a.go") {
		t.Errorf("String = %q", got)
	}
}
