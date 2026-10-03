package logger

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactMasksAssignments(t *testing.T) {
	in := `request headers: api_key=sk-abcdef1234567890 authorization: Bearer xyz-token-value`
	out := Redact(in, nil)
	if strings.Contains(out, "sk-abcdef1234567890") {
		t.Errorf("api key leaked: %s", out)
	}
	if strings.Contains(out, "xyz-token-value") {
		t.Errorf("token leaked: %s", out)
	}
}

func TestRedactMasksKnownSecrets(t *testing.T) {
	out := Redact("using key supersecretvalue123 for request", []string{"supersecretvalue123"})
	if strings.Contains(out, "supersecretvalue123") {
		t.Errorf("known secret leaked: %s", out)
	}
	if !strings.Contains(out, "***") {
		t.Errorf("expected mask: %s", out)
	}
}

func TestRedactKeepsUsefulText(t *testing.T) {
	in := "reading file src/main.go (12 lines)"
	if out := Redact(in, nil); out != in {
		t.Errorf("Redact modified unrelated text: %q -> %q", in, out)
	}
}

func TestLevels(t *testing.T) {
	var buf bytes.Buffer
	l := New(Options{Level: LevelWarn, Stderr: &buf, Redact: true})
	l.Debugf("should not appear")
	l.Infof("should not appear")
	l.Warnf("warn %s", "line")
	l.Errorf("error %d", 7)
	out := buf.String()
	if strings.Contains(out, "should not appear") {
		t.Errorf("level filtering failed:\n%s", out)
	}
	if !strings.Contains(out, "warn line") || !strings.Contains(out, "error 7") {
		t.Errorf("missing expected lines:\n%s", out)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"debug": LevelDebug, "INFO": LevelInfo, "warn": LevelWarn,
		"warning": LevelWarn, "error": LevelError, "off": LevelOff, "nonsense": LevelOff,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
