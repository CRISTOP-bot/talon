package tui

import "testing"

func TestFuzzyPrefixScoresHighest(t *testing.T) {
	candidates := []string{
		"internal/tools/fs.go",
		"internal/tools/tools.go",
		"internal/tui/view.go",
	}
	ranked := Rank("tools", candidates)
	if len(ranked) != 2 {
		t.Fatalf("expected 2 matches, got %v", ranked)
	}
	if ranked[0] != "internal/tools/tools.go" {
		t.Errorf("the best match should be the exact prefix, got %v", ranked)
	}
}

func TestFuzzyWordBoundaryBeatsMidWord(t *testing.T) {
	candidates := []string{"xraytools.go", "tools/editor.go"}
	ranked := Rank("tools", candidates)
	if len(ranked) != 2 {
		t.Fatalf("expected 2 matches, got %v", ranked)
	}
	if ranked[0] != "tools/editor.go" {
		t.Errorf("a boundary match should win, got %v", ranked)
	}
}

func TestFuzzyRejectsNonMatch(t *testing.T) {
	if _, _, ok := Fuzzy("zzz", "internal/main.go"); ok {
		t.Error("a query with no letters in common must not match")
	}
	if _, _, ok := Fuzzy("", "anything"); !ok {
		t.Error("an empty query should match everything")
	}
}

func TestFuzzyIsCaseInsensitive(t *testing.T) {
	if _, _, ok := Fuzzy("FS", "internal/fs.go"); !ok {
		t.Error("matching should ignore case")
	}
}

func TestFuzzyIsDeterministic(t *testing.T) {
	candidates := []string{"a/b.go", "a/b_test.go", "ab.go"}
	first := Rank("b", candidates)
	for i := 0; i < 5; i++ {
		again := Rank("b", candidates)
		for j := range first {
			if first[j] != again[j] {
				t.Fatalf("ranking is not stable: %v vs %v", first, again)
			}
		}
	}
}

func TestHighlightMarksMatches(t *testing.T) {
	p := DarkPalette()
	line := Highlight(p, "fs", "internal/fs.go", false)
	var sb []rune
	for _, s := range line {
		sb = append(sb, []rune(s.Text)...)
	}
	if string(sb) != "internal/fs.go" {
		t.Errorf("highlight changed the text: %q", string(sb))
	}
	// At least one span must be bold.
	bold := false
	for _, s := range line {
		if s.Style.Bold {
			bold = true
		}
	}
	if !bold {
		t.Error("no matched rune was emphasised")
	}
}

func TestHighlightUnknownCandidateIsUntouched(t *testing.T) {
	p := DarkPalette()
	line := Highlight(p, "zzz", "plain.go", false)
	if line.String() != "plain.go" {
		t.Errorf("got %q", line.String())
	}
}
