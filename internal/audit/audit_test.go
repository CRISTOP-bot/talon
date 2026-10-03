package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newLog(t *testing.T, level Level) (*Log, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audit", "audit.jsonl")
	l, err := Open(Options{Path: path, Enabled: true, Level: level, SessionID: "sess-1"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l, path
}

func TestRecordsArePersisted(t *testing.T) {
	l, path := newLog(t, LevelNormal)
	l.ToolAllowed("read_file", "src/main.go")
	l.ToolDenied("delete_file", "a.go", "user rejected")
	l.Network("api.openai.com", "https://api.openai.com/v1/chat/completions")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	events, err := Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events = %d: %+v", len(events), events)
	}
	if events[0].Kind != KindToolExec || events[1].Outcome != OutcomeDenied {
		t.Errorf("events = %+v", events)
	}
	if events[0].Time.IsZero() {
		t.Error("timestamp not set")
	}
	if events[0].Detail["session"] != "sess-1" {
		t.Errorf("session id missing: %v", events[0].Detail)
	}
}

func TestLogFilePermissionsArePrivate(t *testing.T) {
	l, path := newLog(t, LevelNormal)
	l.SessionStarted("openai", "gpt-5")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("audit log permissions = %v, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("audit directory permissions = %v, want 0700", dirInfo.Mode().Perm())
	}
}

func TestSecretsNeverReachTheLog(t *testing.T) {
	l, path := newLog(t, LevelVerbose)
	secret := "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"
	l.Record(Event{
		Kind: KindToolExec, Outcome: OutcomeFailed, Action: "run_command",
		Target: "curl -H 'x-api-key: " + secret + "'",
		Reason: "command failed with " + secret,
		Detail: map[string]any{"output": "leaked " + secret},
	})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "abcdefghijklmnop") {
		t.Errorf("secret reached the audit log:\n%s", data)
	}
	events, _ := Read(path, 0)
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if !strings.Contains(events[0].Reason, "•") {
		t.Errorf("reason not masked: %q", events[0].Reason)
	}
}

func TestLevelFiltering(t *testing.T) {
	l, path := newLog(t, LevelMinimal)
	l.ToolAllowed("read_file", "a.go")    // dropped at minimal
	l.ToolDenied("delete_file", "a", "x") // kept
	l.Close()
	events, _ := Read(path, 0)
	if len(events) != 1 || events[0].Kind != KindToolDeny {
		t.Errorf("minimal level events = %+v", events)
	}

	v, vpath := newLog(t, LevelVerbose)
	v.ToolAllowed("read_file", "a.go")
	v.SecretRedacted("openai-api-key", "log")
	v.Close()
	events, _ = Read(vpath, 0)
	if len(events) != 2 {
		t.Errorf("verbose level events = %+v", events)
	}
}

func TestDisabledLogRecordsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	l, err := Open(Options{Path: path, Enabled: false, Level: LevelVerbose})
	if err != nil {
		t.Fatal(err)
	}
	l.ToolAllowed("read_file", "a.go")
	if l.Enabled() {
		t.Error("a disabled log must not report itself as enabled")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("a disabled log must not create a file")
	}
	// A nil log must be safe to use.
	var nilLog *Log
	nilLog.ToolAllowed("read_file", "a.go")
}

func TestOpenFailureIsReported(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "notadir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(Options{Path: filepath.Join(blocker, "audit.jsonl"), Enabled: true})
	if err == nil {
		t.Fatal("expected an error when the audit directory cannot be created")
	}
}

func TestReadLimitAndCorruption(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.jsonl")
	l, _ := Open(Options{Path: path, Enabled: true, Level: LevelVerbose})
	for i := 0; i < 5; i++ {
		l.ToolAllowed("read_file", "a.go")
	}
	l.Close()

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("garbage\n{}\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	events, err := Read(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Errorf("corrupt line broke parsing: %d events", len(events))
	}
	limited, _ := Read(path, 2)
	if len(limited) != 2 {
		t.Errorf("limit ignored: %d", len(limited))
	}
	if missing, err := Read(filepath.Join(dir, "nope.jsonl"), 0); err != nil || missing != nil {
		t.Errorf("missing log = %v %v", missing, err)
	}
}

func TestCountSizeAndRemove(t *testing.T) {
	l, path := newLog(t, LevelNormal)
	l.ToolAllowed("read_file", "a.go")
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	n, err := Count(path)
	if err != nil || n != 1 {
		t.Errorf("Count = %d (%v)", n, err)
	}
	size, err := Size(path)
	if err != nil || size == 0 {
		t.Errorf("Size = %d (%v)", size, err)
	}
	if err := Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("log not removed")
	}
	if err := Remove(path); err != nil {
		t.Errorf("removing a missing log should be a no-op: %v", err)
	}
	if s, err := Size(filepath.Join(t.TempDir(), "missing.jsonl")); err != nil || s != 0 {
		t.Errorf("Size of a missing log = %d (%v)", s, err)
	}
}

func TestParseLevel(t *testing.T) {
	cases := map[string]Level{
		"minimal": LevelMinimal, "off": LevelMinimal, "normal": LevelNormal,
		"verbose": LevelVerbose, "debug": LevelVerbose, "nonsense": LevelNormal,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
	if LevelNormal.String() != "normal" {
		t.Error("Level.String is wrong")
	}
}

func TestEventStringRenders(t *testing.T) {
	e := Event{
		Time: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		Kind: KindToolDeny, Outcome: OutcomeDenied, Action: "rm", Target: "/", Reason: "denied",
	}
	s := e.String()
	for _, want := range []string{"03:04:05", "tool.deny", "denied", "rm", "/"} {
		if !strings.Contains(s, want) {
			t.Errorf("event string missing %q: %s", want, s)
		}
	}
}

func TestConvenienceRecordersUseDistinctKinds(t *testing.T) {
	l, path := newLog(t, LevelVerbose)
	l.Approval("write_file", true)
	l.ApprovalDenied("write_file", "no")
	l.Network("h", "u")
	l.NetworkBlocked("h", "blocked")
	l.SecretRedacted("k", "w")
	l.Injection("README.md", "policy-tampering@3")
	l.Sandbox("landlock")
	l.PolicyChange("level", "confirm", "full-access")
	l.Consent("1.1.0", "terms")
	l.DataRemoved("sessions")
	l.UpdateVerified("talon", "sha256 ok")
	l.SessionStarted("openai", "gpt-5")
	l.Close()

	events, _ := Read(path, 0)
	seen := map[Kind]bool{}
	for _, e := range events {
		seen[e.Kind] = true
	}
	for _, k := range []Kind{
		KindApproval, KindNetwork, KindNetworkBlock, KindSecretMask, KindInjection,
		KindSandbox, KindPolicyChange, KindConsent, KindDataRemoval, KindUpdate, KindSession,
	} {
		if !seen[k] {
			t.Errorf("kind %s never recorded", k)
		}
	}
}
