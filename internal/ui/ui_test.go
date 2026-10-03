package ui

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/CRISTOP-bot/talon/internal/diff"
)

func mono() Theme { return MonoTheme() }

func TestRenderMarkdownBlocks(t *testing.T) {
	p := NewPrinter(&bytes.Buffer{}, &bytes.Buffer{}, mono())
	src := "# Title\n\nSome **bold** and *italic* and `code`.\n\n" +
		"- first\n- second\n\n" +
		"1. one\n2. two\n\n" +
		"> quoted line\n\n" +
		"```go\nfunc main() {}\n```\n\n" +
		"| a | b |\n|---|---|\n| 1 | 2 |\n"
	out := p.RenderMarkdown(src)

	for _, want := range []string{
		"Title", "Some bold and italic and `code`.",
		"• first", "• second", "1. one", "2. two",
		"│ quoted line", "func main() {}",
		"a", "b",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
	if strings.Contains(out, "```") {
		t.Errorf("code fence markers leaked:\n%s", out)
	}
}

func TestRenderMarkdownHeadingsAndRules(t *testing.T) {
	p := NewPrinter(&bytes.Buffer{}, &bytes.Buffer{}, mono())
	out := p.RenderMarkdown("# H1\n\ntext\n\n---\n\n## H2\n")
	if !strings.Contains(out, "H1") || !strings.Contains(out, "H2") {
		t.Errorf("headings missing:\n%s", out)
	}
	if !strings.Contains(out, "────") {
		t.Errorf("rule missing:\n%s", out)
	}
}

func TestRenderMarkdownUnclosedFenceStillShowsCode(t *testing.T) {
	p := NewPrinter(&bytes.Buffer{}, &bytes.Buffer{}, mono())
	out := p.RenderMarkdown("```python\nprint('x')\n")
	if !strings.Contains(out, "print('x')") {
		t.Errorf("unclosed fence dropped content:\n%s", out)
	}
}

func TestInlineFormatting(t *testing.T) {
	th := DefaultTheme()
	got := StripANSI(th.Inline("a **b** c *d* `e` [link](http://x)"))
	if got != "a b c d `e` link (http://x)" {
		t.Errorf("Inline = %q", got)
	}
}

func TestInlineUnbalancedMarkers(t *testing.T) {
	th := DefaultTheme()
	got := StripANSI(th.Inline("2 * 3 = 6 and `unclosed"))
	if !strings.Contains(got, "2 * 3 = 6") {
		t.Errorf("Inline mangled plain text: %q", got)
	}
}

func TestHighlightGo(t *testing.T) {
	th := DefaultTheme()
	code := "package main\n\n// entry\nfunc main() {\n\tprintln(\"hi\", 42)\n}\n"
	out := Highlight("go", code, th)
	if StripANSI(out) != code {
		t.Errorf("highlight altered code:\n%q\n%q", StripANSI(out), code)
	}
	if out == code {
		t.Error("expected ANSI styling for go code")
	}
	if !strings.Contains(StripANSI(out), "func main()") {
		t.Error("code lost")
	}
}

func TestHighlightPreservesTextForEveryLanguage(t *testing.T) {
	th := DefaultTheme()
	code := "if (x) { y = \"a\"; } // note\n"
	for _, lang := range []string{"c", "cpp", "python", "rust", "javascript", "shell", "", "unknown-lang"} {
		if got := StripANSI(Highlight(lang, code, th)); got != code {
			t.Errorf("lang %q changed code: %q", lang, got)
		}
	}
}

func TestHighlightBlockComments(t *testing.T) {
	th := DefaultTheme()
	code := "/* start\n   middle */\nint x = 1;"
	if got := StripANSI(Highlight("c", code, th)); got != code {
		t.Errorf("block comment handling changed code: %q", got)
	}
}

func TestRenderDiff(t *testing.T) {
	p := NewPrinter(&bytes.Buffer{}, &bytes.Buffer{}, DefaultTheme())
	var buf bytes.Buffer
	fd := DiffFromText("src/main.go", "package main\n\nfunc main() {}\n", "package main\n\nfunc main() {\n\tprintln(1)\n}\n", 3)
	p.RenderDiff(&buf, []diff.FileDiff{fd}, 3)
	out := StripANSI(buf.String())
	if !strings.Contains(out, "src/main.go") {
		t.Errorf("path missing:\n%s", out)
	}
	if !strings.Contains(out, "@@") {
		t.Errorf("hunk header missing:\n%s", out)
	}
	if !strings.Contains(out, "+") || !strings.Contains(out, "println(1)") {
		t.Errorf("added line missing:\n%s", out)
	}
}

func TestMarkdownStreamFlushesLines(t *testing.T) {
	var buf bytes.Buffer
	s := NewMarkdownStream(&buf, mono(), 80)
	chunks := []string{"# Title\n\nHello ", "**world**\n```go\nfunc x() {}\n```\n", "tail"}
	for _, c := range chunks {
		if _, err := s.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	if !strings.Contains(buf.String(), "Hello ") {
		t.Errorf("no incremental output: %q", buf.String())
	}
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"Title", "Hello world", "func x() {}", "tail"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestMarkdownStreamIsIdempotentAfterFlush(t *testing.T) {
	var buf bytes.Buffer
	s := NewMarkdownStream(&buf, mono(), 80)
	_, _ = s.Write([]byte("text"))
	_ = s.Flush()
	before := buf.Len()
	_, _ = s.Write([]byte("more"))
	if buf.Len() != before {
		t.Error("writes after Flush must be ignored")
	}
}

func TestSpinnerWritesAndStops(t *testing.T) {
	var buf bytes.Buffer
	sp := NewSpinner(&buf, DefaultTheme(), true)
	sp.Interval = time.Millisecond
	sp.Start("working")
	sp.Update("still working")
	time.Sleep(5 * time.Millisecond)
	sp.Stop("done")
	if !strings.Contains(buf.String(), "done") {
		t.Errorf("final message missing: %q", buf.String())
	}
	if !strings.Contains(buf.String(), "working") {
		t.Errorf("animated message missing: %q", buf.String())
	}
}

func TestSpinnerDisabledPrintsOnlyTheFinalLine(t *testing.T) {
	var buf bytes.Buffer
	sp := NewSpinner(&buf, DefaultTheme(), false)
	sp.Start("quiet")
	sp.Stop("final")
	out := buf.String()
	if !strings.Contains(out, "final") {
		t.Errorf("the final line must be shown even without animation: %q", out)
	}
	if strings.Contains(out, "◐") {
		t.Errorf("no frames should be drawn when disabled: %q", out)
	}
}

func TestPrinterTableAndKV(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, &buf, mono())
	p.Table([]string{"name", "value"}, [][]string{{"a", "1"}, {"longer", "22"}})
	out := buf.String()
	if !strings.Contains(out, "name") || !strings.Contains(out, "longer") {
		t.Errorf("table missing content:\n%s", out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 {
		t.Errorf("table lines = %d, want 4 (header, separator, 2 rows)", len(lines))
	}
	buf.Reset()
	p.KV([][2]string{{"key", "value"}})
	if !strings.Contains(buf.String(), "key") {
		t.Errorf("kv missing:\n%s", buf.String())
	}
}

func TestPrinterPanel(t *testing.T) {
	var buf bytes.Buffer
	p := NewPrinter(&buf, &buf, mono())
	p.Panel("title", []string{"line one", "line two"})
	out := buf.String()
	// The mono theme uses ASCII borders, the coloured theme uses box drawing.
	for _, want := range []string{"title", "line one", "line two", "+-", "|"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q:\n%s", want, out)
		}
	}
}

func TestWrapText(t *testing.T) {
	p := NewPrinter(&bytes.Buffer{}, &bytes.Buffer{}, mono())
	p.Width = 20
	got := p.WrapText(strings.Repeat("word ", 12))
	for _, line := range strings.Split(got, "\n") {
		if len(line) > 25 {
			t.Errorf("line not wrapped: %q", line)
		}
	}
}

func TestStripANSI(t *testing.T) {
	if got := StripANSI("\x1b[1;31mred\x1b[0m"); got != "red" {
		t.Errorf("StripANSI = %q", got)
	}
	if DisplayWidth("\x1b[31mabc\x1b[0m") != 3 {
		t.Error("DisplayWidth should ignore escapes")
	}
}
