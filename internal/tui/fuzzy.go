package tui

import (
	"sort"
	"strings"
)

// Fuzzy matches a query against a candidate, returning a score, the positions
// of the matched runes and whether the query matched at all.
//
// The weights encode what a developer expects from a file picker: a match at the
// very start beats a match in the middle of a word, a contiguous run beats
// scattered letters, and a match in the file name beats one buried in the
// directory path. That is what makes "tools" pick tools.go over tools/fs.go.
func Fuzzy(query, candidate string) (int, []int, bool) {
	if query == "" {
		return 0, nil, true
	}
	q := []rune(strings.ToLower(query))
	c := []rune(candidate)
	baseStart := baseOffset(candidate)
	positions := make([]int, 0, len(q))
	score := 0
	ci := 0
	runLen := 0
	runStart := -1
	// The greedy match can land in the directory ("internal/tools/…") even when
	// the file name is the better answer, so the name is scored on its own.
	base := strings.ToLower(baseName(candidate))
	lowerQuery := strings.ToLower(query)
	if base != "" && strings.Contains(base, lowerQuery) {
		score += 10
		if strings.HasPrefix(base, lowerQuery) {
			score += 6
		}
	}
	for _, qr := range q {
		found := -1
		for ; ci < len(c); ci++ {
			if runeFold(c[ci]) == qr {
				found = ci
				break
			}
		}
		if found < 0 {
			return 0, nil, false
		}
		positions = append(positions, found)

		contiguous := len(positions) > 1 && positions[len(positions)-2] == found-1
		if contiguous {
			runLen++
			if runStart < 0 {
				runStart = found - runLen
			}
			// A flat bonus per extra rune: an escalating one let a long run
			// anywhere outrank a match at the start of the name.
			score += 3
		} else {
			runLen, runStart = 1, found
			switch {
			case found == 0:
				score += 20
			case isBoundary(c, found):
				score += 10
			case isSeparator(c[found-1]):
				score += 6
			}
			// Awarded once per run, when the run starts.
			if found >= baseStart {
				if found == baseStart {
					score += 8
				} else {
					score += 5
				}
			}
		}
		score -= found / 8
		ci++
	}
	return score, positions, true
}

func isSeparator(r rune) bool {
	return r == '/' || r == '.' || r == '_' || r == '-'
}

// baseName returns the file name without its directories.
func baseName(candidate string) string {
	if i := strings.LastIndex(candidate, "/"); i >= 0 {
		return candidate[i+1:]
	}
	return candidate
}

// baseOffset returns the index where the file name starts.
func baseOffset(candidate string) int {
	if i := strings.LastIndex(candidate, "/"); i >= 0 {
		return len([]rune(candidate[:i+1]))
	}
	return 0
}

func isBoundary(c []rune, i int) bool {
	if i == 0 {
		return true
	}
	prev := c[i-1]
	return prev == ' ' || prev == '/' || prev == '.' || prev == '_' || prev == '-'
}

func runeFold(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

// Rank filters and orders candidates by fuzzy score, best first.
func Rank(query string, candidates []string) []string {
	type scored struct {
		name  string
		score int
	}
	out := make([]scored, 0, len(candidates))
	for _, c := range candidates {
		s, _, ok := Fuzzy(query, c)
		if !ok {
			continue
		}
		out = append(out, scored{c, s})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].score != out[j].score {
			return out[i].score > out[j].score
		}
		return out[i].name < out[j].name
	})
	names := make([]string, len(out))
	for i, s := range out {
		names[i] = s.name
	}
	return names
}

// Highlight renders a candidate with the matched runes emphasised, which is what
// makes a fuzzy picker usable.
func Highlight(p Palette, query, candidate string, selected bool) Line {
	base := p.Text
	if selected {
		base = p.Selection
	}
	if query == "" {
		return Line{{Text: candidate, Style: base}}
	}
	_, positions, ok := Fuzzy(query, candidate)
	if !ok {
		return Line{{Text: candidate, Style: base}}
	}
	marks := make(map[int]bool, len(positions))
	for _, p := range positions {
		marks[p] = true
	}
	runes := []rune(candidate)
	line := make(Line, 0, len(runes))
	for i, r := range runes {
		st := base
		if marks[i] {
			st = base
			st.Bold = true
			if selected {
				st = Style{Fg: Black, Bg: BrightCyan, Bold: true}
			} else {
				st = Style{Fg: BrightCyan, Bold: true}
			}
		}
		line = append(line, Span{Text: string(r), Style: st})
	}
	return line
}
