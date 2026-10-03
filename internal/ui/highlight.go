package ui

import (
	"strings"
)

// Highlight applies syntax colouring to source code. Language detection is by
// explicit name or file extension; unknown languages fall back to generic
// token colouring so output is still readable.
func Highlight(lang, code string, t Theme) string {
	if t.NoColor() || code == "" {
		return code
	}
	l := normalizeLang(lang)
	switch l {
	case "go":
		return highlightGeneric(code, t, goKeywords)
	case "rust", "rs":
		return highlightGeneric(code, t, rustKeywords)
	case "python", "py":
		return highlightGeneric(code, t, pythonKeywords)
	case "js", "javascript", "ts", "typescript", "jsx", "tsx", "json", "yaml", "toml", "sh", "bash", "shell", "zsh", "ruby", "rb", "kotlin", "kt", "java", "c", "cpp", "c++", "csharp", "cs", "swift", "php", "sql", "make", "dockerfile", "html", "css":
		return highlightGeneric(code, t, nil)
	default:
		return highlightGeneric(code, t, nil)
	}
}

// NormalizeLang is the exported form of normalizeLang.
func NormalizeLang(lang string) string { return normalizeLang(lang) }

func normalizeLang(lang string) string {
	l := strings.ToLower(strings.TrimSpace(lang))
	l = strings.TrimPrefix(l, ".")
	switch l {
	case "golang":
		return "go"
	case "rs":
		return "rust"
	case "py":
		return "python"
	case "js":
		return "javascript"
	case "ts":
		return "typescript"
	case "sh", "zsh":
		return "shell"
	case "cpp", "c++", "cc", "hpp":
		return "cpp"
	case "cs":
		return "csharp"
	case "yml":
		return "yaml"
	}
	return l
}

var (
	goKeywords = map[string]bool{
		"func": true, "package": true, "import": true, "var": true, "const": true, "type": true,
		"struct": true, "interface": true, "map": true, "chan": true, "go": true, "defer": true,
		"if": true, "else": true, "for": true, "range": true, "switch": true, "case": true,
		"default": true, "return": true, "break": true, "continue": true, "fallthrough": true,
		"select": true, "goto": true, "nil": true, "true": true, "false": true,
	}
	rustKeywords = map[string]bool{
		"fn": true, "let": true, "mut": true, "const": true, "static": true, "struct": true,
		"enum": true, "impl": true, "trait": true, "for": true, "while": true, "loop": true,
		"if": true, "else": true, "match": true, "pub": true, "use": true, "mod": true,
		"crate": true, "self": true, "super": true, "where": true, "return": true, "async": true,
		"await": true, "move": true, "ref": true, "dyn": true, "type": true, "as": true,
		"true": true, "false": true, "unsafe": true, "in": true,
	}
	pythonKeywords = map[string]bool{
		"def": true, "class": true, "import": true, "from": true, "if": true, "elif": true,
		"else": true, "for": true, "while": true, "return": true, "yield": true, "with": true,
		"as": true, "try": true, "except": true, "finally": true, "raise": true, "lambda": true,
		"pass": true, "break": true, "continue": true, "and": true, "or": true, "not": true,
		"in": true, "is": true, "None": true, "True": true, "False": true, "self": true,
		"async": true, "await": true, "global": true, "nonlocal": true, "del": true,
	}
)

// tokenKind classifies a lexical token for colouring.
type tokenKind int

const (
	tokPlain tokenKind = iota
	tokKeyword
	tokString
	tokNumber
	tokComment
	tokType
	tokFunc
)

// highlightGeneric tokenises code line by line and colours it. It understands
// C-family string/comment syntax, hash comments, and brace/paren nesting well
// enough to be useful across the languages Talon ships with.
func highlightGeneric(code string, t Theme, keywords map[string]bool) string {
	lines := strings.Split(code, "\n")
	out := make([]string, 0, len(lines))
	inBlockComment := false

	for _, line := range lines {
		out = append(out, highlightLine(line, t, keywords, &inBlockComment))
	}
	return strings.Join(out, "\n")
}

//nolint:gocyclo // a tokenizer is naturally branchy
func highlightLine(line string, t Theme, keywords map[string]bool, inBlockComment *bool) string {
	var b strings.Builder
	runes := []rune(line)
	i := 0

	for i < len(runes) {
		r := runes[i]
		switch {
		case *inBlockComment:
			end := -1
			for j := i; j+1 < len(runes); j++ {
				if runes[j] == '*' && runes[j+1] == '/' {
					end = j + 2
					break
				}
			}
			if end < 0 {
				b.WriteString(t.Paint(t.Muted, string(runes[i:])))
				return b.String()
			}
			b.WriteString(t.Paint(t.Muted, string(runes[i:end])))
			*inBlockComment = false
			i = end
		case (r == '/' && i+1 < len(runes) && runes[i+1] == '/') && !isInString(runes, i):
			b.WriteString(t.Paint(t.Muted, string(runes[i:])))
			return b.String()
		case (r == '#' && i == 0 && len(runes) > 1 && runes[1] == '!'):
			b.WriteString(t.Paint(t.Muted, string(runes[i:])))
			return b.String()
		case r == '"' || r == '\'' || r == '`':
			// Consume the whole string literal, honouring escapes.
			j := i + 1
			for j < len(runes) {
				if runes[j] == '\\' {
					j += 2
					continue
				}
				if runes[j] == r {
					j++
					break
				}
				j++
			}
			if j > len(runes) {
				j = len(runes)
			}
			b.WriteString(t.Paint(t.DiffAdd, string(runes[i:j])))
			i = j
		case isIdentStart(r):
			j := i
			for j < len(runes) && isIdentPart(runes[j]) {
				j++
			}
			word := string(runes[i:j])
			i = j
			switch {
			case keywords[word]:
				b.WriteString(t.Paint(t.Accent+t.Bold, word))
			case isTypeWord(word):
				b.WriteString(t.Paint(t.Primary, word))
			case j < len(runes) && runes[j] == '(':
				b.WriteString(t.Paint(t.Warning, word))
			case word == "true" || word == "false" || word == "nil" || word == "None":
				b.WriteString(t.Paint(t.DiffMeta, word))
			default:
				b.WriteString(word)
			}
		case r >= '0' && r <= '9':
			j := i
			for j < len(runes) && (isIdentPart(runes[j]) || runes[j] == '.') {
				j++
			}
			b.WriteString(t.Paint(t.Warning, string(runes[i:j])))
			i = j
		default:
			b.WriteRune(r)
			i++
		}
	}
	return b.String()
}

func isInString(runes []rune, idx int) bool {
	var q rune
	for i := 0; i < idx; i++ {
		switch runes[i] {
		case '\\':
			i++
		case '"', '\'', '`':
			if q == 0 {
				q = runes[i]
			} else if q == runes[i] {
				q = 0
			}
		}
	}
	return q != 0
}

func isIdentStart(r rune) bool {
	return r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r > 127
}

func isIdentPart(r rune) bool { return isIdentStart(r) || (r >= '0' && r <= '9') }

func isTypeWord(w string) bool {
	if w == "" {
		return false
	}
	r := []rune(w)[0]
	return r >= 'A' && r <= 'Z'
}
