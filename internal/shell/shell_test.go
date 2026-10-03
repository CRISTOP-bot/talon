package shell

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/talon-cli/talon/internal/errs"
)

func TestRunCapturesOutput(t *testing.T) {
	r := NewRunner()
	res, err := r.Run(context.Background(), Request{Command: "echo hello"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Errorf("stdout = %q", res.Stdout)
	}
	if res.ExitCode != 0 || !res.Success() {
		t.Errorf("result = %+v", res)
	}
	if res.Duration <= 0 {
		t.Error("duration was not measured")
	}
}

func TestRunReportsFailures(t *testing.T) {
	r := NewRunner()
	res, err := r.Run(context.Background(), Request{Command: "exit 7"})
	if err != nil {
		t.Fatalf("a failing command is not a runner error: %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit code = %d", res.ExitCode)
	}
	if res.Success() {
		t.Error("Success must be false")
	}
}

func TestRunCapturesStderr(t *testing.T) {
	r := NewRunner()
	res, _ := r.Run(context.Background(), Request{Command: "echo oops 1>&2"})
	if !strings.Contains(res.Stderr, "oops") {
		t.Errorf("stderr = %q", res.Stderr)
	}
	if !strings.Contains(res.Combined(), "oops") {
		t.Errorf("Combined = %q", res.Combined())
	}
}

func TestRunInDirectory(t *testing.T) {
	dir := t.TempDir()
	r := NewRunner()
	res, err := r.Run(context.Background(), Request{Command: "pwd", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.TrimSpace(res.Stdout), dir) {
		t.Errorf("cwd = %q, want %q", res.Stdout, dir)
	}
}

func TestTimeout(t *testing.T) {
	r := &Runner{Shell: "/bin/sh"}
	start := time.Now()
	res, err := r.Run(context.Background(), Request{Command: "sleep 5", TimeoutSeconds: 1})
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !res.TimedOut {
		t.Error("result should be marked as timed out")
	}
	if res.ExitCode != 124 {
		t.Errorf("exit code = %d", res.ExitCode)
	}
	if time.Since(start) > 4*time.Second {
		t.Errorf("timeout took too long: %s", time.Since(start))
	}
}

func TestStreamingCallback(t *testing.T) {
	var chunks []string
	r := NewRunner()
	_, err := r.Run(context.Background(), Request{
		Command:  "echo one; echo two",
		OnOutput: func(stream, chunk string) { chunks = append(chunks, strings.TrimSpace(chunk)) },
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(chunks, "|")
	if !strings.Contains(joined, "one") || !strings.Contains(joined, "two") {
		t.Errorf("chunks = %v", chunks)
	}
}

func TestOutputTruncation(t *testing.T) {
	r := &Runner{Shell: "/bin/sh"}
	res, err := r.Run(context.Background(), Request{
		Command:        "yes aaaaaaaaaaaaaaaaaaaaaaaaaaaa | head -500",
		MaxOutputBytes: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) > 400 {
		t.Errorf("output not truncated: %d bytes", len(res.Stdout))
	}
	if !strings.Contains(res.Stdout, "truncated") {
		t.Errorf("truncation was not announced: %q", res.Stdout)
	}
}

func TestEnvOverride(t *testing.T) {
	r := NewRunner()
	res, _ := r.Run(context.Background(), Request{
		Command: "echo $TALON_TEST_VALUE",
		Env:     map[string]string{"TALON_TEST_VALUE": "set-by-talon"},
	})
	if !strings.Contains(res.Stdout, "set-by-talon") {
		t.Errorf("env not applied: %q", res.Stdout)
	}
}

func TestEmptyCommand(t *testing.T) {
	if _, err := NewRunner().Run(context.Background(), Request{Command: "  "}); err == nil {
		t.Error("expected an error for an empty command")
	}
}

func TestContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	res, err := NewRunner().Run(ctx, Request{Command: "sleep 5"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if errs.KindOf(err) != errs.KindCancelled {
		t.Errorf("kind = %v (%v)", errs.KindOf(err), err)
	}
	if res.TimedOut {
		t.Error("an external cancellation is not an internal timeout")
	}
	if res.ExitCode == 0 {
		t.Errorf("the command should have been killed: %+v", res)
	}
	if time.Since(start) > 2*time.Second {
		t.Errorf("cancellation took too long: %s", time.Since(start))
	}
}

func TestHistory(t *testing.T) {
	r := NewRunner()
	_, _ = r.Run(context.Background(), Request{Command: "echo one"})
	_, _ = r.Run(context.Background(), Request{Command: "echo two"})
	hist := r.History()
	if len(hist) != 2 || hist[1] != "echo two" {
		t.Errorf("history = %v", hist)
	}
}
