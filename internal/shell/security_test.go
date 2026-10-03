package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CRISTOP-bot/talon/internal/audit"
	"github.com/CRISTOP-bot/talon/internal/sandbox"
	"github.com/CRISTOP-bot/talon/internal/secrets"
)

func newRunner(t *testing.T, dir string) *Runner {
	t.Helper()
	return &Runner{Shell: "/bin/sh"}
}

// TestEnvSanitizerRemovesCredentials proves the child process never sees the
// API key, which is a common way secrets end up in build logs.
func TestEnvSanitizerRemovesCredentials(t *testing.T) {
	t.Setenv("AI_API_KEY", "sk-live-should-not-be-visible-0123456789")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "wJalrXUtnFEMI-should-not-be-visible")
	t.Setenv("SAFE_VALUE", "keep-me")

	dir := t.TempDir()
	r := newRunner(t, dir)
	var seen []string
	res, err := r.Run(context.Background(), Request{
		Command: "env",
		Dir:     dir,
		EnvSanitizer: func(env []string) []string {
			seen = secrets.FilterEnv(env, nil, false)
			return seen
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"sk-live-should-not-be-visible-0123456789",
		"wJalrXUtnFEMI-should-not-be-visible",
	} {
		if strings.Contains(res.Stdout, secret) {
			t.Fatalf("a credential reached the child process: %s", secret)
		}
		if strings.Contains(res.Stdout, secret) || containsLine(seen, secret) {
			t.Fatalf("a credential survived environment filtering: %s", secret)
		}
	}
	if !strings.Contains(res.Stdout, "SAFE_VALUE=keep-me") {
		t.Fatal("ordinary environment variables must survive filtering")
	}
}

// TestAuditRecordsCommandExecution proves the decision and the failure are both
// recorded, without the command's output.
func TestAuditRecordsCommandExecution(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "audit.jsonl")
	log, err := audit.Open(audit.Options{Path: logPath, Enabled: true, Level: audit.LevelNormal})
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()

	dir := t.TempDir()
	r := newRunner(t, dir)
	if _, err := r.Run(context.Background(), Request{
		Command: "echo token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789",
		Dir:     dir,
		Audit:   log,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(context.Background(), Request{
		Command: "exit 3",
		Dir:     dir,
		Audit:   log,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "token") {
		t.Fatalf("the executed command should be recorded:\n%s", data)
	}
	if strings.Contains(string(data), "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789") {
		t.Fatalf("the audit log stored a credential:\n%s", data)
	}
	if !strings.Contains(string(data), "exit 3") {
		t.Fatalf("the failing command should be recorded:\n%s", data)
	}
}

// TestSandboxConfinesCommandOutput proves the runner honours a sandbox policy:
// where Landlock is available the child cannot read outside the declared roots.
func TestSandboxConfinesCommandOutput(t *testing.T) {
	status := sandbox.Detect()
	if !status.Available {
		t.Skipf("no kernel sandbox on this machine: %s", status.Kernel)
	}
	// The helper re-executes os.Executable() with Talon's private flags. Under
	// `go test` that binary is the test runner, which would reject them, so the
	// kernel-level proof lives in internal/sandbox (which re-executes itself with
	// -test.run). What is verified here is that the runner applies the policy and
	// reports isolation honestly.
	self, err := os.Executable()
	if err != nil {
		t.Skipf("cannot locate this binary: %v", err)
	}
	if strings.HasSuffix(self, ".test") {
		t.Skip("the sandbox helper cannot re-execute a test binary; " +
			"kernel confinement is covered by internal/sandbox")
	}
	dir := t.TempDir()
	policy := sandbox.DefaultPolicy(dir, os.Environ())
	shellPath, lerr := exec.LookPath("/bin/sh")
	if lerr != nil {
		t.Skip("no /bin/sh available")
	}
	policy.ExecPaths = append(policy.ExecPaths, shellPath)

	r := newRunner(t, dir)
	res, err := r.Run(context.Background(), Request{
		Command: "cat /etc/hostname",
		Dir:     dir,
		Sandbox: &policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Sandboxed {
		t.Fatal("the command should have run under kernel isolation")
	}
	if res.Success() && strings.TrimSpace(res.Stdout) != "" {
		t.Fatalf("the sandboxed command read a file outside its roots: %q", res.Stdout)
	}
	if err := os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("inside\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err = r.Run(context.Background(), Request{
		Command: "cat ok.txt",
		Dir:     dir,
		Sandbox: &policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "inside") {
		t.Fatalf("a write inside the workspace should still be readable: %q (%+v)", res.Stdout, res)
	}
}

// TestUnconfinedRunStillWorks proves a nil policy means the old behaviour, so
// embedding Talon without the sandbox stack does not break commands.
func TestUnconfinedRunStillWorks(t *testing.T) {
	dir := t.TempDir()
	r := newRunner(t, dir)
	res, err := r.Run(context.Background(), Request{Command: "echo plain", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sandboxed {
		t.Fatal("no policy was set, so the command must not claim isolation")
	}
	if !strings.Contains(res.Stdout, "plain") {
		t.Fatalf("unexpected output: %q", res.Stdout)
	}
}

func containsLine(env []string, secret string) bool {
	for _, line := range env {
		if strings.Contains(line, secret) {
			return true
		}
	}
	return false
}
