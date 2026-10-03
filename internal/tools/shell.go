package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/talon-cli/talon/internal/llm"
	"github.com/talon-cli/talon/internal/perm"
	"github.com/talon-cli/talon/internal/shell"
)

// ShellDefinitions returns the command execution tools.
func ShellDefinitions() []*Definition {
	return []*Definition{
		runCommandDef(),
		runTestsDef(),
		buildProjectDef(),
	}
}

func runCommandDef() *Definition {
	return &Definition{
		Name: "run_command",
		Description: "Run a shell command in the project directory and return its output and exit code. " +
			"Use it for git, package managers, linters and any CLI tool. Long-running or interactive " +
			"commands are not supported. Destructive commands require user confirmation.",
		Parameters: llm.JSONSchema(map[string]any{
			"command":         llm.Prop("string", "The command line to execute, e.g. `cargo test`"),
			"timeout_seconds": map[string]any{"type": "integer", "description": "Kill the command after this many seconds", "minimum": 1},
			"cwd":             llm.Prop("string", "Directory relative to the project root (default: root)"),
		}, "command"),
		Risk:       perm.RiskExec,
		CommandArg: "command",
		Summarize:  summarizeWith("command", "Run `%s`"),
		Handler:    handleRunCommand,
	}
}

type runCommandArgs struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Cwd            string `json:"cwd"`
}

func handleRunCommand(ctx *Context, raw json.RawMessage) (Result, error) {
	var a runCommandArgs
	if err := decode("run_command", raw, &a); err != nil {
		return Result{}, err
	}
	return runShellCommand(ctx, a.Command, a.Cwd, a.TimeoutSeconds, "run_command")
}

func runTestsDef() *Definition {
	return &Definition{
		Name: "run_tests",
		Description: "Run the project's test suite. When no command is given, the detected test runner " +
			"for the project is used (cargo test, go test ./..., npm test, pytest, ...).",
		Parameters: llm.JSONSchema(map[string]any{
			"command":         llm.Prop("string", "Explicit test command; omit to use the detected runner"),
			"timeout_seconds": map[string]any{"type": "integer", "description": "Kill the run after this many seconds", "minimum": 1},
		}),
		Risk:       perm.RiskExec,
		CommandArg: "command",
		Summarize:  summarizeTests,
		Handler:    handleRunTests,
	}
}

func summarizeTests(raw json.RawMessage) string {
	var a runCommandArgs
	if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Command) == "" {
		return "Run the test suite"
	}
	return fmt.Sprintf("Run tests: `%s`", a.Command)
}

func handleRunTests(ctx *Context, raw json.RawMessage) (Result, error) {
	var a runCommandArgs
	if err := decode("run_tests", raw, &a); err != nil {
		return Result{}, err
	}
	command := strings.TrimSpace(a.Command)
	if command == "" {
		command = detectedTestCommand(ctx)
		if command == "" {
			return Result{}, argError("run_tests",
				"no test runner was detected; pass an explicit command")
		}
	}
	out, err := runShellCommand(ctx, command, "", a.TimeoutSeconds, "run_tests")
	if err != nil {
		return Result{}, err
	}
	out.Content = fmt.Sprintf("Test command: %s\nExit code: %d\n\n%s", command,
		intFromMetadata(out.Metadata), out.Content)
	return out, nil
}

func detectedTestCommand(ctx *Context) string {
	if ctx.Project != nil {
		if ctx.Project.TestCommand != "" {
			return ctx.Project.TestCommand
		}
		if ctx.Project.PackageManager != nil && ctx.Project.PackageManager.Test != "" {
			return ctx.Project.PackageManager.Test
		}
	}
	if hasFile(filepath.Join(ctx.Workspace, "Makefile"), "test:") {
		return "make test"
	}
	return ""
}

// intFromMetadata reads an int value from a result's metadata.
func intFromMetadata(m map[string]any, key ...string) int {
	name := "exit_code"
	if len(key) > 0 {
		name = key[0]
	}
	if v, ok := m[name].(int); ok {
		return v
	}
	return 0
}

func hasFile(path, needle string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), needle)
}

func buildProjectDef() *Definition {
	return &Definition{
		Name: "build_project",
		Description: "Compile or build the project using the detected build system, and return the " +
			"compiler output. Use this to verify that changes compile.",
		Parameters: llm.JSONSchema(map[string]any{
			"command":         llm.Prop("string", "Explicit build command; omit to use the detected one"),
			"timeout_seconds": map[string]any{"type": "integer", "description": "Kill the build after this many seconds", "minimum": 1},
		}),
		Risk:       perm.RiskExec,
		CommandArg: "command",
		Summarize:  summarizeBuild,
		Handler:    handleBuild,
	}
}

func summarizeBuild(raw json.RawMessage) string {
	var a runCommandArgs
	if err := json.Unmarshal(raw, &a); err != nil || strings.TrimSpace(a.Command) == "" {
		return "Build the project"
	}
	return fmt.Sprintf("Build: `%s`", a.Command)
}

func handleBuild(ctx *Context, raw json.RawMessage) (Result, error) {
	var a runCommandArgs
	if err := decode("build_project", raw, &a); err != nil {
		return Result{}, err
	}
	command := strings.TrimSpace(a.Command)
	if command == "" {
		command = detectedBuildCommand(ctx)
		if command == "" {
			return Result{}, argError("build_project",
				"no build system was detected; pass an explicit command")
		}
	}
	out, err := runShellCommand(ctx, command, "", a.TimeoutSeconds, "build_project")
	if err != nil {
		return Result{}, err
	}
	out.Content = fmt.Sprintf("Build command: %s\nExit code: %d\n\n%s", command,
		intFromMetadata(out.Metadata), out.Content)
	return out, nil
}

func detectedBuildCommand(ctx *Context) string {
	if ctx.Project != nil && ctx.Project.BuildCommand != "" {
		return ctx.Project.BuildCommand
	}
	return ""
}

// runShellCommand is the shared implementation behind the three shell tools.
func runShellCommand(ctx *Context, command, cwd string, timeout int, tool string) (Result, error) {
	dir := ctx.Workspace
	if strings.TrimSpace(cwd) != "" && cwd != "." {
		abs, err := ctx.resolvePath(cwd)
		if err != nil {
			return Result{}, err
		}
		dir = abs
	}
	if timeout <= 0 {
		timeout = ctx.Limits.CommandTimeoutSeconds
	}
	req := shell.Request{
		Command:        command,
		Dir:            dir,
		TimeoutSeconds: timeout,
		OnOutput:       ctx.OnCommandOutput,
		MaxOutputBytes: ctx.Limits.MaxOutputBytes,
		EnvSanitizer:   ctx.Gates.EnvSanitizer,
		Audit:          ctx.Gates.Audit,
	}
	if ctx.Gates.SandboxPolicy != nil {
		req.Sandbox = ctx.Gates.SandboxPolicy()
	}
	res, err := ctx.Shell.Run(ctx.Ctx, req)
	if err != nil {
		// A failing command is information for the model, not a tool error, so
		// the output is returned alongside the exit status when available.
		if res.Command == "" {
			return Result{}, err
		}
		return shellResult(ctx, res, command), nil
	}
	return shellResult(ctx, res, command), nil
}

func shellResult(ctx *Context, res shell.Result, command string) Result {
	out := res.Combined()
	status := "succeeded"
	if !res.Success() {
		status = "failed"
	}
	content := fmt.Sprintf("$ %s\n%s (exit code %d, %s)", command, status, res.ExitCode, res.Duration.Round(time.Millisecond))
	if out != "" {
		content += "\n\n" + out
	}
	// Command output is untrusted data: it may contain credentials and it may
	// contain text aimed at the agent.
	if ctx.Gates.SensitiveContent != nil {
		cleaned, notice, cerr := ctx.Gates.SensitiveContent(command, out)
		if cerr != nil {
			// Command output must never reach the model with credentials in it,
			// and it must not be able to end the tool call silently either.
			content += "\n" + cerr.Error()
		} else {
			content = strings.Replace(content, out, cleaned, 1)
			if notice != "" {
				content += "\n" + notice
			}
		}
	}
	if ctx.Gates.ScanInjection != nil {
		if report := ctx.Gates.ScanInjection(command, out); report != "" {
			content += "\n" + report
		}
	}
	display := fmt.Sprintf("%s: %s (exit %d)", command, status, res.ExitCode)
	return Result{
		Content: content,
		Display: display,
		Metadata: map[string]any{
			"command":   command,
			"exit_code": res.ExitCode,
			"timed_out": res.TimedOut,
		},
	}
}
