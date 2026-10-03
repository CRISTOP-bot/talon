package ui

import "strings"

// Inline renders inline markdown spans (bold, italic, code, links) with theme
// styling. Code spans are protected from further styling.
func (t Theme) Inline(s string) string {
	var b strings.Builder
	runes := []rune(s)
	for i := 0; i < len(runes); {
		switch {
		case runes[i] == '`':
			if end := indexRune(runes, '`', i+1); end > 0 {
				code := string(runes[i+1 : end])
				b.WriteString(t.Paint(t.Code+t.Bold, "`"+code+"`"))
				i = end + 1
				continue
			}
			b.WriteRune(runes[i])
			i++
		case hasPrefixRunes(runes, i, "**"):
			if end := indexRunes(runes, "**", i+2); end > 0 {
				b.WriteString(t.Paint(t.Bold, string(runes[i+2:end])))
				i = end + 2
				continue
			}
			b.WriteString("**")
			i += 2
		case runes[i] == '*' || runes[i] == '_':
			marker := runes[i]
			if end := indexRune(runes, marker, i+1); end > 0 {
				b.WriteString(t.Paint(t.Italic, string(runes[i+1:end])))
				i = end + 1
				continue
			}
			b.WriteRune(marker)
			i++
		case runes[i] == '[':
			if labelEnd, end, target := parseLink(runes, i); end > 0 {
				label := string(runes[i+1 : labelEnd])
				if t.Link != "" {
					b.WriteString(t.Paint(t.Link, label))
				} else {
					b.WriteString(label)
				}
				if target != "" && target != label {
					b.WriteString(t.Faint(" (" + target + ")"))
				}
				i = end
				continue
			}
			b.WriteRune(runes[i])
			i++
		case runes[i] == '\\' && i+1 < len(runes):
			b.WriteRune(runes[i+1])
			i += 2
		default:
			b.WriteRune(runes[i])
			i++
		}
	}
	return b.String()
}

func hasPrefixRunes(runes []rune, i int, s string) bool {
	sr := []rune(s)
	if i+len(sr) > len(runes) {
		return false
	}
	for k, r := range sr {
		if runes[i+k] != r {
			return false
		}
	}
	return true
}

func indexRune(runes []rune, target rune, from int) int {
	for i := from; i < len(runes); i++ {
		if runes[i] == target {
			return i
		}
	}
	return -1
}

func indexRunes(runes []rune, target string, from int) int {
	for i := from; i < len(runes); i++ {
		if hasPrefixRunes(runes, i, target) {
			return i
		}
	}
	return -1
}

// parseLink locates a "[label](target)" span starting at i. It returns the
// index of the closing bracket, the index just past the closing parenthesis,
// and the target.
func parseLink(runes []rune, i int) (labelEnd, end int, target string) {
	j := i + 1
	for j < len(runes) && runes[j] != ']' {
		j++
	}
	if j >= len(runes) || j+1 >= len(runes) || runes[j+1] != '(' {
		return 0, -1, ""
	}
	k := j + 2
	for k < len(runes) && runes[k] != ')' {
		k++
	}
	if k >= len(runes) {
		return 0, -1, ""
	}
	return j, k + 1, string(runes[j+2 : k])
}
