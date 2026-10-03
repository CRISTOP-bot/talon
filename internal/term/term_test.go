package term

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDecodeControlKeys(t *testing.T) {
	cases := []struct {
		in   []byte
		want Key
		n    int
	}{
		{[]byte{0x03}, KeyCtrlC, 1},
		{[]byte{0x04}, KeyCtrlD, 1},
		{[]byte{'\r'}, KeyEnter, 1},
		{[]byte{'\n'}, KeyEnter, 1},
		{[]byte{'\t'}, KeyTab, 1},
		{[]byte{0x7f}, KeyBackspace, 1},
		{[]byte("a"), KeyRune, 1},
		{[]byte("ñ"), KeyRune, 2},
		{[]byte{0x1b}, KeyEsc, 1},
	}
	for _, c := range cases {
		ev, n, err := Decode(c.in)
		if err != nil {
			t.Fatalf("Decode(%v): %v", c.in, err)
		}
		if ev.Key != c.want {
			t.Errorf("Decode(%v).Key = %v, want %v", c.in, ev.Key, c.want)
		}
		if n != c.n {
			t.Errorf("Decode(%v) consumed %d, want %d", c.in, n, c.n)
		}
	}
}

func TestDecodeEscapeSequences(t *testing.T) {
	cases := []struct {
		in   string
		want Key
	}{
		{"\x1b[A", KeyUp},
		{"\x1b[B", KeyDown},
		{"\x1b[C", KeyRight},
		{"\x1b[D", KeyLeft},
		{"\x1bOA", KeyUp},
		{"\x1b[H", KeyHome},
		{"\x1b[F", KeyEnd},
		{"\x1b[3~", KeyDelete},
		{"\x1b[5~", KeyPageUp},
		{"\x1b[6~", KeyPageDown},
		{"\x1b[1~", KeyHome},
	}
	for _, c := range cases {
		ev, n, err := Decode([]byte(c.in))
		if err != nil {
			t.Fatalf("Decode(%q): %v", c.in, err)
		}
		if ev.Key != c.want {
			t.Errorf("Decode(%q).Key = %v, want %v", c.in, ev.Key, c.want)
		}
		if n != len(c.in) {
			t.Errorf("Decode(%q) consumed %d, want %d", c.in, n, len(c.in))
		}
	}
}

func TestDecodeAltRune(t *testing.T) {
	ev, n, err := Decode([]byte("\x1bb"))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Key != KeyRune || ev.Rune != 'b' || !ev.Alt {
		t.Errorf("got %+v", ev)
	}
	if n != 2 {
		t.Errorf("consumed %d, want 2", n)
	}
}

func TestNeedsContinuation(t *testing.T) {
	yes := []string{"foo(", "x = [", "if (a {", `println("`, "cmd \\", "items,", "a &&", "func f() {"}
	no := []string{"", "hello world", "func f() {}", "done.", "a = b"}
	for _, in := range yes {
		if !NeedsContinuation(in) {
			t.Errorf("NeedsContinuation(%q) = false, want true", in)
		}
	}
	for _, in := range no {
		if NeedsContinuation(in) {
			t.Errorf("NeedsContinuation(%q) = true, want false", in)
		}
	}
}

func TestLongestCommonPrefix(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{[]string{"read_file", "read_file_range"}, "read_file"},
		{[]string{"abc", "abd"}, "ab"},
		{[]string{"one"}, "one"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := longestCommonPrefix(c.in); got != c.want {
			t.Errorf("longestCommonPrefix(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestVisibleWidth(t *testing.T) {
	if got := visibleWidth("abc"); got != 3 {
		t.Errorf("visibleWidth(abc) = %d", got)
	}
	if got := visibleWidth("\x1b[31mabc\x1b[0m"); got != 3 {
		t.Errorf("styled width = %d, want 3", got)
	}
	if got := visibleWidth("日本"); got != 4 {
		t.Errorf("wide runes = %d, want 4", got)
	}
}

func TestHistoryPersistenceAndDedup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "history")
	h := NewHistory(path, 10)
	h.Add("first command")
	h.Add("second command")
	h.Add("first command") // duplicate of a non-adjacent line is kept
	h.Add("   ")           // blanks ignored
	if h.Len() != 3 {
		t.Fatalf("Len = %d, want 3", h.Len())
	}
	if h.At(0) != "first command" {
		t.Errorf("At(0) = %q", h.At(0))
	}
	h.Add("first command") // immediate duplicate is dropped
	if h.Len() != 3 {
		t.Errorf("duplicate not dropped: %d", h.Len())
	}
	if err := h.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	reloaded := NewHistory(path, 10)
	if reloaded.Len() != 3 {
		t.Errorf("reloaded Len = %d", reloaded.Len())
	}
	if got := reloaded.Search("second"); len(got) != 1 || got[0] != "second command" {
		t.Errorf("Search = %v", got)
	}
	// Saving twice must not duplicate entries.
	if err := reloaded.Save(); err != nil {
		t.Fatal(err)
	}
	if again := NewHistory(path, 10); again.Len() != 3 {
		t.Errorf("double save duplicated entries: %d", again.Len())
	}
}

func TestReadLinePlainWhenNotATerminal(t *testing.T) {
	dir := t.TempDir()
	inPath := filepath.Join(dir, "in")
	if err := os.WriteFile(inPath, []byte("hello\nworld\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(inPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out testWriter
	rl := NewReadline(f, &out, nil)
	line, err := rl.ReadLine("> ")
	if err != nil {
		t.Fatal(err)
	}
	if line != "hello" {
		t.Errorf("line = %q", line)
	}
	if out.s != "> " {
		t.Errorf("prompt not written: %q", out.s)
	}
	line, err = rl.ReadLine("> ")
	if err != nil || line != "world" {
		t.Errorf("second line = %q err = %v", line, err)
	}
}

type testWriter struct{ s string }

func (w *testWriter) Write(p []byte) (int, error) {
	w.s += string(p)
	return len(p), nil
}
