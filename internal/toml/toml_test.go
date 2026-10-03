package toml

import (
	"strings"
	"testing"
)

func TestParseScalarsAndTables(t *testing.T) {
	doc := `
# a comment
title = "talon"
count = 42
ratio = 1.5
enabled = true
tags = ["go", "cli", "agent"]
empty = []
literal = 'C:\path\no-escape'
escaped = "line\nbreak\t\"quoted\" A"

[agent]
model = "test-model"
max_steps = 12

[agent.limits]
max_output_bytes = 262144

[permissions]
level = "confirm"
deny = ["rm -rf", "sudo"]
`
	root, err := Parse(doc)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s, _ := root.Get("title"); s.Kind != KindString || s.Str != "talon" {
		t.Errorf("title = %#v", s)
	}
	if n, _ := root.Get("count"); n.Int != 42 {
		t.Errorf("count = %v", n.Int)
	}
	if f, _ := root.Get("ratio"); f.Float != 1.5 {
		t.Errorf("ratio = %v", f.Float)
	}
	if b, _ := root.Get("enabled"); !b.Bool {
		t.Error("enabled should be true")
	}
	tags, ok := root.Get("tags")
	if !ok {
		t.Fatal("tags missing")
	}
	got, ok := tags.AsStrings()
	if !ok || len(got) != 3 || got[2] != "agent" {
		t.Errorf("tags = %v", got)
	}
	if e, _ := root.Get("empty"); len(e.Array) != 0 {
		t.Errorf("empty should be an empty array, got %v", e.Array)
	}
	if l, _ := root.Get("literal"); l.Str != `C:\path\no-escape` {
		t.Errorf("literal = %q", l.Str)
	}
	if s, _ := root.Get("escaped"); s.Str != "line\nbreak\t\"quoted\" A" {
		t.Errorf("escaped = %q", s.Str)
	}
	if s, _ := root.Get("agent.model"); s.Str != "test-model" {
		t.Errorf("agent.model = %q", s.Str)
	}
	if n, _ := root.Get("agent.max_steps"); n.Int != 12 {
		t.Errorf("agent.max_steps = %v", n.Int)
	}
	if n, _ := root.Get("agent.limits.max_output_bytes"); n.Int != 262144 {
		t.Errorf("nested value = %v", n.Int)
	}
	lvl, _ := root.Get("permissions.level")
	if lvl.Str != "confirm" {
		t.Errorf("permissions.level = %q", lvl.Str)
	}
	deny, _ := root.Get("permissions.deny")
	items, _ := deny.AsStrings()
	if len(items) != 2 || items[0] != "rm -rf" {
		t.Errorf("permissions.deny = %v", items)
	}
}

func TestParseInlineTableAndDottedKeys(t *testing.T) {
	root, err := Parse(`
a.b.c = "deep"
inline = { x = 1, y = "two", nested = { z = true } }
arr = [
  "one",
  "two",
]
multi = """
line1
line2"""
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s, _ := root.Get("a.b.c"); s.Str != "deep" {
		t.Errorf("dotted key = %q", s.Str)
	}
	if n, _ := root.Get("inline.x"); n.Int != 1 {
		t.Error("inline.x wrong")
	}
	if b, _ := root.Get("inline.nested.z"); !b.Bool {
		t.Error("inline nested wrong")
	}
	arr, _ := root.Get("arr")
	items, _ := arr.AsStrings()
	if len(items) != 2 || items[1] != "two" {
		t.Errorf("multiline array = %v", items)
	}
	if s, _ := root.Get("multi"); s.Str != "line1\nline2" {
		t.Errorf("multiline string = %q", s.Str)
	}
}

func TestParseArrayOfTables(t *testing.T) {
	root, err := Parse(`
[[plugin]]
name = "docker"
enabled = true

[[plugin]]
name = "github"
enabled = false
`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p, ok := root.Get("plugin")
	if !ok || p.Kind != KindArray || len(p.Array) != 2 {
		t.Fatalf("plugin array = %#v", p)
	}
	if s, _ := p.Array[0].Get("name"); s.Str != "docker" {
		t.Errorf("first plugin = %q", s.Str)
	}
	if b, _ := p.Array[1].Get("enabled"); b.Bool {
		t.Error("second plugin should be disabled")
	}
}

func TestParseErrors(t *testing.T) {
	cases := []string{
		"key without value",
		"key = ",
		"[unterminated\n",
		"a = 1\na = 2\n",
		"[t]\n[t]\n",
		"a = [1, 2",
		`a = "unterminated`,
		"a = @nope",
	}
	for _, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Errorf("expected error for input %q", c)
		}
	}
}

func TestSetGetDelete(t *testing.T) {
	root := NewTable()
	root.Set("model.provider", String("openai"))
	root.Set("model.name", String("gpt-x"))
	root.Set("permissions.level", String("auto"))
	if s, _ := root.Get("model.provider"); s.Str != "openai" {
		t.Fatalf("provider = %q", s.Str)
	}
	root.Set("model.provider", String("anthropic"))
	if s, _ := root.Get("model.provider"); s.Str != "anthropic" {
		t.Fatalf("overwrite failed: %q", s.Str)
	}
	root.Delete("model.name")
	if root.Has("model.name") {
		t.Error("delete failed")
	}
	if !root.Has("model.provider") {
		t.Error("sibling key was lost")
	}
	// The original tree value must survive.
	if s, _ := root.Get("model.provider"); s.Str != "anthropic" {
		t.Fatalf("original mutated: %q", s.Str)
	}
}

func TestCloneIsDeep(t *testing.T) {
	root := NewTable()
	root.Set("a.b", Strings([]string{"x"}))
	cp := root.Clone()
	cp.Set("a.b", Strings([]string{"y"}))
	if s, _ := root.Get("a.b"); s.Array[0].Str != "x" {
		t.Error("clone shares state with source")
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	src := `# managed by talon
model = "gpt-x"
temperature = 0.7

[permissions]
level = "confirm"
deny = ["sudo"]

[agent]
max_steps = 20
`
	root, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	out := Marshal(root)
	if !strings.Contains(string(out), "level = \"confirm\"") {
		t.Errorf("missing scalar:\n%s", out)
	}
	if !strings.Contains(string(out), "[permissions]") {
		t.Errorf("missing table header:\n%s", out)
	}
	// Round trip must be stable.
	again, err := Parse(string(out))
	if err != nil {
		t.Fatalf("re-Parse: %v\n%s", err, out)
	}
	if a, b := string(Marshal(root)), string(Marshal(again)); a != b {
		t.Errorf("marshal not idempotent:\n%s\n---\n%s", a, b)
	}
}

func TestMarshalEscapesKeysAndValues(t *testing.T) {
	root := NewTable()
	root.Set("weird key", String("line\nbreak"))
	out := string(Marshal(root))
	if !strings.Contains(out, "\"weird key\" = \"line\\nbreak\"") {
		t.Errorf("escaping wrong:\n%s", out)
	}
}
