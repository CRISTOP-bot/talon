package ui

import (
	"strings"
)

// RenderMarkdown converts a markdown document to styled terminal output. It
// supports headings, bold/italic/code spans, lists, tables, block quotes,
// horizontal rules, links and fenced code blocks with syntax highlighting.
func (p *Printer) RenderMarkdown(src string) string {
	t := p.Theme
	var out []string
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")

	var paragraph []string
	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		out = append(out, t.Inline(strings.Join(paragraph, " ")))
		paragraph = nil
	}

	inCode := false
	var codeLang string
	var codeBody []string
	var listKind string
	var listItems []string
	flushList := func() {
		if len(listItems) == 0 {
			return
		}
		bullet := t.Paint(t.Primary, "•")
		box := t.Paint(t.Primary, "▸")
		for i, item := range listItems {
			marker := bullet
			if listKind == "task" {
				marker = t.Paint(t.Success, GlyphOK)
			} else if listKind == "ordered" {
				marker = t.Paint(t.Primary, itoa(i+1)+".")
			} else if listKind == "quote" {
				marker = t.Paint(t.Muted, "│")
			} else if listKind == "arrow" {
				marker = box
			}
			out = append(out, "  "+marker+" "+t.Inline(item))
		}
		listItems = nil
		listKind = ""
	}

	i := 0
	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Fenced code block.
		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				out = append(out, p.renderCode(codeLang, strings.Join(codeBody, "\n")))
				inCode = false
				codeLang, codeBody = "", nil
			} else {
				flushParagraph()
				flushList()
				inCode = true
				codeLang = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
				codeBody = nil
			}
			i++
			continue
		}
		if inCode {
			codeBody = append(codeBody, line)
			i++
			continue
		}

		// Horizontal rule.
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			flushParagraph()
			flushList()
			out = append(out, p.Theme.BorderLine(strings.Repeat("─", 60)))
			i++
			continue
		}

		// Heading.
		if level := headingLevel(trimmed); level > 0 {
			flushParagraph()
			flushList()
			text := strings.TrimSpace(trimmed[level:])
			switch level {
			case 1:
				out = append(out, t.Head(text))
				out = append(out, t.BorderLine(strings.Repeat("─", len([]rune(text)))))
			case 2:
				out = append(out, t.Paint(t.Bold+t.Primary, text))
			default:
				out = append(out, t.Paint(t.Muted+t.Bold, text))
			}
			i++
			continue
		}

		// Block quote.
		if strings.HasPrefix(trimmed, "> ") || trimmed == ">" {
			flushParagraph()
			flushList()
			listKind = "quote"
			listItems = append(listItems, strings.TrimSpace(strings.TrimPrefix(trimmed, ">")))
			i++
			flushListIfQuoteEnds(lines, i)
			continue
		}

		// Tables: a header row followed by a separator row.
		if strings.Contains(trimmed, "|") && i+1 < len(lines) && isTableSeparator(lines[i+1]) {
			flushParagraph()
			flushList()
			headers := splitTableRow(trimmed)
			i += 2
			var rows [][]string
			for i < len(lines) && strings.Contains(lines[i], "|") {
				rows = append(rows, splitTableRow(lines[i]))
				i++
			}
			out = append(out, renderTable(p.Theme, headers, rows))
			continue
		}

		// Lists.
		if item, kind, ok := parseListItem(trimmed); ok {
			flushParagraph()
			if kind != listKind {
				flushList()
				listKind = kind
			}
			listItems = append(listItems, item)
			i++
			continue
		}
		if trimmed == "" {
			flushParagraph()
			flushList()
			i++
			continue
		}

		flushList()
		paragraph = append(paragraph, trimmed)
		i++
	}
	flushParagraph()
	flushList()
	if inCode && len(codeBody) > 0 {
		out = append(out, p.renderCode(codeLang, strings.Join(codeBody, "\n")))
	}
	return strings.Join(out, "\n")
}

func flushListIfQuoteEnds(lines []string, i int) {
	if i >= len(lines) {
		return
	}
	next := strings.TrimSpace(lines[i])
	if !strings.HasPrefix(next, ">") {
		return
	}
}

func headingLevel(s string) int {
	level := 0
	for level < len(s) && s[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level >= len(s) || s[level] != ' ' {
		return 0
	}
	return level
}

func parseListItem(s string) (item, kind string, ok bool) {
	switch {
	case strings.HasPrefix(s, "- [x] "), strings.HasPrefix(s, "* [x] "):
		return s[6:], "task", true
	case strings.HasPrefix(s, "- [ ] "), strings.HasPrefix(s, "* [ ] "):
		return s[6:], "task", true
	case strings.HasPrefix(s, "- "), strings.HasPrefix(s, "* "):
		return s[2:], "bullet", true
	case strings.HasPrefix(s, "-> "), strings.HasPrefix(s, "→ "):
		return s[3:], "arrow", true
	}
	// Ordered list: "1. " or "12) ".
	j := 0
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j > 0 && j+1 < len(s) && (s[j] == '.' || s[j] == ')') && s[j+1] == ' ' {
		return s[j+2:], "ordered", true
	}
	return "", "", false
}

func isTableSeparator(s string) bool {
	t := strings.TrimSpace(s)
	if !strings.Contains(t, "-") {
		return false
	}
	for _, r := range t {
		if r != '-' && r != '|' && r != ' ' && r != ':' {
			return false
		}
	}
	return strings.Contains(t, "|")
}

func splitTableRow(s string) []string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "|")
	s = strings.TrimSuffix(s, "|")
	parts := strings.Split(s, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func renderTable(t Theme, headers []string, rows [][]string) string {
	pr := &Printer{Out: discard{}, Theme: t}
	var b strings.Builder
	pr.Out = &b
	pr.Table(headers, rows)
	return strings.TrimRight(b.String(), "\n")
}

func (p *Printer) renderCode(lang, body string) string {
	t := p.Theme
	highlighted := Highlight(lang, body, t)
	lines := strings.Split(highlighted, "\n")
	width := 0
	for _, l := range lines {
		if n := DisplayWidth(l); n > width {
			width = n
		}
	}
	head := ""
	if lang != "" {
		head = t.Faint(lang)
	}
	var b strings.Builder
	bar := t.BorderLine("│")
	if head != "" {
		b.WriteString(t.BorderLine("┌─") + head + t.BorderLine(strings.Repeat("─", max(3, width-DisplayWidth(lang)+4))) + "\n")
	} else {
		b.WriteString(t.BorderLine("┌"+strings.Repeat("─", max(3, width+4))) + "\n")
	}
	for _, l := range lines {
		pad := width - DisplayWidth(l)
		if pad < 0 {
			pad = 0
		}
		b.WriteString(bar + " " + l + strings.Repeat(" ", pad) + " " + bar + "\n")
	}
	b.WriteString(t.BorderLine("└" + strings.Repeat("─", max(3, width+4))))
	return b.String()
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
