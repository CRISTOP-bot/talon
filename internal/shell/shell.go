// Package shell runs commands on the user's behalf with a timeout, streaming
// output and a captured transcript.
package shell

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/talon-cli/talon/internal/audit"
	"github.com/talon-cli/talon/internal/errs"
	"github.com/talon-cli/talon/internal/injection"
	"github.com/talon-cli/talon/internal/sandbox"
	"github.com/talon-cli/talon/internal/secrets"
)

// SandboxPolicy is the confinement applied to a command. It is an alias so
// callers outside this package do not need to import the sandbox package.
type SandboxPolicy = sandbox.Policy

// Result is the outcome of a command.
type Result struct {
	Command  string
	ExitCode int
	Stdout   string
	Stderr   string
	// Duration is the wall-clock time the command took.
	Duration time.Duration
	// Sandboxed is true when the command ran under kernel isolation. It is
	// never claimed unless the sandbox was actually applied.
	Sandboxed bool
	// TimedOut is true when the command was killed by the context.
	TimedOut bool
}

// Combined returns stdout and stderr joined for display to the model.
func (r Result) Combined() string {
	var b strings.Builder
	if r.Stdout != "" {
		b.WriteString(r.Stdout)
		if !strings.HasSuffix(r.Stdout, "\n") {
			b.WriteString("\n")
		}
	}
	if r.Stderr != "" {
		b.WriteString(r.Stderr)
	}
	return strings.TrimRight(b.String(), "\n")
}

// Success reports whether the command exited with status 0.
func (r Result) Success() bool { return r.ExitCode == 0 && !r.TimedOut }

// Request describes a command to run.
type Request struct {
	// Command is the command line, run through the user's shell.
	Command string
	// Dir is the working directory; empty means the workspace.
	Dir string
	// TimeoutSeconds bounds the run; 0 means the package default.
	TimeoutSeconds int
	// Env adds environment variables on top of the process environment.
	Env map[string]string
	// OnOutput receives output chunks as they arrive, for live streaming.
	OnOutput func(stream string, chunk string)
	// MaxOutputBytes truncates captured output; 0 means the package default.
	MaxOutputBytes int
	// Sandbox confines the command; nil means no kernel isolation.
	Sandbox *sandbox.Policy
	// EnvSanitizer filters the environment before the command runs. It
	// receives the process environment and returns what the child may see.
	EnvSanitizer func(env []string) []string
	// Audit records the execution locally.
	Audit *audit.Log
	// Isolated is set to the result of the kernel sandbox attempt.
	Isolated bool
}

// String renders a request for the audit log without credentials.
func (r Request) String() string {
	cmd, _ := secrets.Redact(r.Command, secrets.Bullets)
	return fmt.Sprintf("cmd=%q dir=%q isolated=%t", cmd, r.Dir, r.Isolated)
}

// DefaultTimeout is used when a request does not specify one.
const DefaultTimeout = 600

// DefaultMaxOutput is the capture limit applied when none is given.
const DefaultMaxOutput = 200000

// Runner executes shell commands.
type Runner struct {
	// Shell is the shell binary used; empty means the platform default.
	Shell string
	mu    sync.Mutex
	// history keeps the last commands for `talon shell history`.
	history []string
}

// NewRunner creates a Runner using the user's shell.
func NewRunner() *Runner {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	return &Runner{Shell: shell}
}

// shellArgs returns the arguments used to run a command line.
func (r *Runner) shellArgs() []string {
	if strings.HasSuffix(r.Shell, "fish") || strings.HasSuffix(r.Shell, "nu") ||
		strings.HasSuffix(r.Shell, "csh") || strings.HasSuffix(r.Shell, "powershell") ||
		strings.HasSuffix(r.Shell, "pwsh") {
		return []string{"-c"}
	}
	return []string{"-c"}
}

// Run executes a request.
// RunCommandLine is the shell this runner was built for.
func (r *Runner) ShellPath() string { return r.Shell }

func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.Command) == "" {
		return Result{}, errs.Usage("the command is empty")
	}
	timeout := req.TimeoutSeconds
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	maxOut := req.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = DefaultMaxOutput
	}

	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	dir := req.Dir
	if dir == "" {
		dir, _ = os.Getwd()
	}
	// The child environment is built explicitly so credentials can be removed
	// before the command ever sees them.
	env := os.Environ()
	for k, v := range req.Env {
		env = append(env, k+"="+v)
	}
	if req.EnvSanitizer != nil {
		env = req.EnvSanitizer(env)
	}

	var cmd *exec.Cmd
	isolated := false
	if req.Sandbox != nil {
		policy := *req.Sandbox
		policy.WorkingDir = dir
		policy.Env = env
		// The shell itself must be executable inside the sandbox.
		if shellPath, lerr := exec.LookPath(r.Shell); lerr == nil {
			policy.ExecPaths = append(policy.ExecPaths, shellPath)
		}
		built, used, serr := sandbox.Command(policy, r.Shell, append(r.shellArgs(), req.Command))
		if serr != nil {
			return Result{}, errs.Internal("shell", "cannot prepare the sandbox: %v", serr)
		}
		if used {
			cmd = built
			isolated = true
		}
	}
	if cmd == nil {
		cmd = exec.CommandContext(runCtx, r.Shell, append(r.shellArgs(), req.Command)...)
		cmd.Env = env
		cmd.Dir = dir
		configureProcess(cmd)
	}
	req.Isolated = isolated
	if req.Audit != nil {
		req.Audit.ToolAllowed("shell", "isolated="+boolText(isolated)+" "+oneLine(req.Command))
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, errs.Internal("shell", "cannot capture stdout: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return Result{}, errs.Internal("shell", "cannot capture stderr: %v", err)
	}
	started := time.Now()
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return Result{}, errs.NotFound("shell", "shell %q was not found", r.Shell)
		}
		return Result{}, errs.Permission("shell", "cannot start the command: %v", err)
	}

	// A watchdog closes the pipes if the command ignores the kill signal.
	stopWatchdog := make(chan struct{})
	defer close(stopWatchdog)
	go func() {
		select {
		case <-runCtx.Done():
			killTree(cmd)
		case <-stopWatchdog:
		}
	}()

	res := Result{Command: req.Command}
	var outBuf, errBuf strings.Builder
	var mu sync.Mutex
	var truncated bool

	pump := func(r io.Reader, name string) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			line := sc.Text() + "\n"
			mu.Lock()
			limitHit := outBuf.Len()+errBuf.Len()+len(line) > maxOut
			if !limitHit {
				if name == "stdout" {
					outBuf.WriteString(line)
				} else {
					errBuf.WriteString(line)
				}
			} else if !truncated {
				truncated = true
				note := fmt.Sprintf("\n[talon] output truncated at %d bytes\n", maxOut)
				if name == "stdout" {
					outBuf.WriteString(note)
				} else {
					errBuf.WriteString(note)
				}
			}
			mu.Unlock()
			if req.OnOutput != nil {
				req.OnOutput(name, line)
			}
		}
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pump(stdout, "stdout") }()
	go func() { defer wg.Done(); pump(stderr, "stderr") }()
	wg.Wait()

	waitErr := cmd.Wait()
	// A timeout or cancellation must take the whole process group down, or a
	// background child keeps the pipes open and Run blocks until it exits.
	if runCtx.Err() != nil {
		killTree(cmd)
	}
	mu.Lock()
	res.Stdout, res.Stderr = outBuf.String(), errBuf.String()
	mu.Unlock()
	res.Duration = time.Since(started)
	res.ExitCode = exitCode(waitErr)
	if runCtx.Err() != nil && errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		res.TimedOut = true
		res.ExitCode = 124
	}

	if req.Audit != nil && !res.Success() {
		req.Audit.ToolFailed("shell", oneLine(req.Command), fmt.Sprintf("exit %d", res.ExitCode))
	}

	res.Sandboxed = isolated

	r.mu.Lock()
	r.history = append(r.history, req.Command)
	if len(r.history) > 100 {
		r.history = r.history[len(r.history)-100:]
	}
	r.mu.Unlock()

	if res.TimedOut {
		return res, errs.Newf(errs.KindTimeout, "shell",
			"command timed out after %ds", timeout)
	}
	if ctx.Err() != nil {
		return res, errs.Cancelled("shell", "command cancelled")
	}
	return res, nil
}

// ScanOutput inspects captured command output for credentials and for content
// that looks like an instruction aimed at the agent. Command output is
// untrusted: a build log can contain a prompt-injection payload.
func ScanOutput(output string, source string) sensitive {
	d := sensitiveResult(output, source)
	return d
}

// sensitiveResult is a small wrapper so callers do not need two imports.
type sensitive struct {
	Findings []string
	Kinds    []string
	Notice   string
}

func sensitiveResult(output, source string) sensitive {
	var out sensitive
	if f := secrets.ScanLines(output); len(f) > 0 {
		for _, x := range f {
			out.Findings = append(out.Findings, x.Kind)
		}
	}
	if inj := injection.Scan(output); len(inj) > 0 {
		out.Kinds = out.Kinds[:0]
		for _, x := range inj {
			out.Kinds = append(out.Kinds, x.Pattern)
		}
		out.Notice = injection.Notice(inj)
	}
	_ = source
	return out
}

func boolText(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func oneLine(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

// History returns the commands run in this session.
func (r *Runner) History() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.history...)
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return 1
}
