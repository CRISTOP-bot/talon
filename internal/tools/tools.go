// Package tools implements the capability layer the model uses to act on a
// project. Every tool is a self-contained definition with a JSON schema, a risk
// level, argument validation and a handler.
//
// The model never touches the filesystem or a shell directly: it can only call
// a registered tool, and every call goes through the permission policy.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/talon-cli/talon/internal/audit"
	"github.com/talon-cli/talon/internal/diff"
	"github.com/talon-cli/talon/internal/errs"
	"github.com/talon-cli/talon/internal/git"
	"github.com/talon-cli/talon/internal/index"
	"github.com/talon-cli/talon/internal/journal"
	"github.com/talon-cli/talon/internal/llm"
	"github.com/talon-cli/talon/internal/logger"
	"github.com/talon-cli/talon/internal/perm"
	"github.com/talon-cli/talon/internal/project"
	"github.com/talon-cli/talon/internal/shell"
)

// SourceBuiltin labels tools that ship with Talon; plugin and MCP tools carry
// "plugin:<name>" and "mcp:<name>".
const SourceBuiltin = "builtin"

// Limits bounds tool behaviour.
type Limits struct {
	// MaxOutputBytes truncates the text returned to the model.
	MaxOutputBytes int
	// MaxReadBytes is the largest file read_file will load.
	MaxReadBytes int64
	// CommandTimeoutSeconds bounds shell tools.
	CommandTimeoutSeconds int
	// DefaultContextLines is the diff context width.
	DefaultContextLines int
}

// DefaultLimits returns the built-in limits.
func DefaultLimits() Limits {
	return Limits{
		MaxOutputBytes:        20000,
		MaxReadBytes:          2 * 1024 * 1024,
		CommandTimeoutSeconds: 600,
		DefaultContextLines:   3,
	}
}

// Security gates shared by every tool. They are interfaces so the tools
// package does not depend on the security stack directly, which keeps the
// dependency direction one-way.
type Gates struct {
	// SensitivePath decides whether a file may be read at all.
	SensitivePath func(path string) error
	// SensitiveContent sanitises content before it reaches the model. It
	// returns the text to send and an optional notice to append. A refusal is
	// signalled by errRefused with a reason, which must stop the tool: returning
	// an empty string would otherwise look like an empty file.
	SensitiveContent func(source, body string) (out string, notice string, err error)
	// WrapUntrusted marks content as data with its provenance.
	WrapUntrusted func(source, content string) string
	// ScanInjection reports suspicious instructions found in content.
	ScanInjection func(source, content string) string
	// SandboxPolicy builds the confinement policy for shell commands.
	SandboxPolicy func() *shell.SandboxPolicy
	// EnvSanitizer filters the environment handed to child processes.
	EnvSanitizer func(env []string) []string
	// Audit records security decisions; may be nil.
	Audit *audit.Log
	// PrivacyMode reports whether persistence and logging are disabled.
	PrivacyMode bool
}

// Context carries everything a tool needs. It is created once per session.
type Context struct {
	Ctx       context.Context
	Workspace string
	Limits    Limits
	Policy    *perm.Policy
	Shell     *shell.Runner
	Git       *git.Client
	Index     *index.Index
	Project   *project.Info
	Journal   *journal.Journal
	Log       *logger.Logger
	// Gates are the security controls; they are optional so the tools package
	// can be used without the security stack (tests, embedding).
	Gates Gates
	// OnCommandOutput streams shell output to the UI.
	OnCommandOutput func(stream, chunk string)
}

// Result is what a tool returns to the agent.
type Result struct {
	// Content is the text handed to the model.
	Content string
	// Display is a short line for the terminal (optional).
	Display string
	// Diff is set by tools that modified files, so the UI can show a patch.
	Diff *diff.FileDiff
	// Changed lists workspace-relative paths that were modified.
	Changed []string
	// Metadata carries structured data for the agent engine and the UI.
	Metadata map[string]any
}

// Definition is one tool.
type Definition struct {
	// Name is the identifier the model calls.
	Name string
	// Description explains what the tool does and when to use it. It is sent
	// to the model verbatim, so it must be precise.
	Description string
	// Parameters is the JSON schema for the arguments object.
	Parameters map[string]any
	// Risk is the permission class of the tool.
	Risk perm.Risk
	// Source is "builtin", "plugin:<name>" or "mcp:<name>", shown in /tools.
	Source string
	// Handler executes the tool.
	Handler func(ctx *Context, args json.RawMessage) (Result, error)
	// Summarize renders a one-line description of a concrete call, used in the
	// confirmation prompt. It defaults to the raw arguments.
	Summarize func(args json.RawMessage) string
	// PathArg names the argument holding a filesystem path, if any.
	PathArg string
	// CommandArg names the argument holding a shell command, if any.
	CommandArg string
	// ReadOnly marks tools that never modify anything, used by read-only mode.
	ReadOnly bool
}

// Spec renders the tool for the model.
func (d *Definition) Spec() llm.ToolSpec {
	params := d.Parameters
	if params == nil {
		params = llm.JSONSchema(nil)
	}
	return llm.ToolSpec{Name: d.Name, Description: d.Description, Parameters: params}
}

// Request builds the permission request for a concrete call.
func (d *Definition) Request(args json.RawMessage) perm.Request {
	req := perm.Request{Tool: d.Name, Risk: d.Risk}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(args, &m); err == nil {
		if d.PathArg != "" {
			_ = json.Unmarshal(m[d.PathArg], &req.Path)
		}
		if d.CommandArg != "" {
			_ = json.Unmarshal(m[d.CommandArg], &req.Command)
		}
	}
	if d.ReadOnly {
		req.Risk = perm.RiskRead
	}
	req.Description = d.Summarize(args)
	return req
}

// Run executes the tool after validating that arguments decode.
func (d *Definition) Run(ctx *Context, args json.RawMessage) (Result, error) {
	if args == nil || len(args) == 0 || string(args) == "null" {
		args = json.RawMessage("{}")
	}
	var probe map[string]any
	if err := json.Unmarshal(args, &probe); err != nil {
		return Result{}, fmt.Errorf("arguments must be a JSON object: %w", err)
	}
	if err := Validate(d.Parameters, probe); err != nil {
		return Result{}, err
	}
	if ctx.Log != nil {
		ctx.Log.Debugf("tool %s args=%s", d.Name, truncate(string(args), 400))
	}
	res, err := d.Handler(ctx, args)
	if err != nil {
		return Result{}, err
	}
	res.Content = Truncate(res.Content, ctx.Limits.MaxOutputBytes)
	return res, nil
}

// Registry holds the tools available to the model.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Definition
	order []string
}

// NewRegistry creates an empty registry.
// Get returns a built-in tool definition by name.
func Get(name string) (*Definition, bool) {
	for _, def := range All() {
		if def.Name == name {
			return def, true
		}
	}
	return nil, false
}

func NewRegistry() *Registry {
	return &Registry{tools: map[string]*Definition{}}
}

// Register adds tools, replacing any with the same name.
func (r *Registry) Register(defs ...*Definition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, d := range defs {
		if d.Source == "" {
			d.Source = SourceBuiltin
		}
		if _, exists := r.tools[d.Name]; !exists {
			r.order = append(r.order, d.Name)
		} else {
			d.Source = r.tools[d.Name].Source + " (replaced)"
		}
		r.tools[d.Name] = d
	}
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (*Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.tools[name]
	return d, ok
}

// Names returns the registered tool names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]string(nil), r.order...)
}

// All returns every tool, sorted by name.
func (r *Registry) All() []*Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Definition, 0, len(r.tools))
	for _, d := range r.tools {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Specs renders the tools for the model.
func (r *Registry) Specs() []llm.ToolSpec {
	out := make([]llm.ToolSpec, 0, len(r.order))
	for _, name := range r.Names() {
		if d, ok := r.Get(name); ok {
			out = append(out, d.Spec())
		}
	}
	return out
}

// Len returns the number of registered tools.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// Truncate shortens text to n bytes on a line boundary and says so.
func Truncate(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	cut := s[:n]
	if idx := strings.LastIndex(cut, "\n"); idx > n/2 {
		cut = cut[:idx]
	}
	return cut + fmt.Sprintf("\n… [truncated, %d of %d bytes shown]", len(cut), len(s))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// argError formats a validation error with the tool name.
func argError(tool, format string, args ...any) error {
	return fmt.Errorf("%s: %s", tool, fmt.Sprintf(format, args...))
}

// decode unmarshals arguments into v with a tool-prefixed error.
func decode(tool string, args json.RawMessage, v any) error {
	if err := json.Unmarshal(args, v); err != nil {
		return argError(tool, "invalid arguments: %v", err)
	}
	return nil
}

// requireFields checks that the required string arguments are non-empty.
func requireFields(tool string, m map[string]any, names ...string) error {
	for _, n := range names {
		v, ok := m[n]
		if !ok {
			return argError(tool, "missing required argument %q", n)
		}
		s, ok := v.(string)
		if !ok {
			return argError(tool, "argument %q must be a string", n)
		}
		if strings.TrimSpace(s) == "" {
			return argError(tool, "argument %q must not be empty", n)
		}
	}
	return nil
}

// GuardPath applies the sensitive-file gate to a workspace-relative path.
func (c *Context) GuardPath(rel string) error {
	if c.Gates.SensitivePath == nil {
		return nil
	}
	return c.Gates.SensitivePath(rel)
}

// Sanitize prepares content for the model: secrets masked, provenance added,
// injection notice appended. A gate that refuses the content returns an error,
// so the caller never receives the data.
func (c *Context) Sanitize(source, content string) (string, error) {
	cleaned := content
	if c.Gates.SensitiveContent != nil {
		text, notice, err := c.Gates.SensitiveContent(source, content)
		if err != nil {
			return "", err
		}
		cleaned = text
		if notice != "" {
			cleaned += "\n" + notice
		}
	}
	if c.Gates.WrapUntrusted != nil {
		cleaned = c.Gates.WrapUntrusted(source, cleaned)
	}
	return cleaned, nil
}

// resolvePath turns a workspace-relative path into an absolute one, refusing to
// escape the project directory.
func (c *Context) resolvePath(rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", errs.Usage("the path must not be empty")
	}
	if filepath.IsAbs(rel) {
		abs := filepath.Clean(rel)
		if !c.withinWorkspace(abs) {
			return "", errs.Permission("tools", "%s is outside the project directory %s", abs, c.Workspace)
		}
		return abs, nil
	}
	abs := filepath.Clean(filepath.Join(c.Workspace, filepath.FromSlash(rel)))
	if !c.withinWorkspace(abs) {
		return "", errs.Permission("tools", "%s resolves outside the project directory", rel)
	}
	return abs, nil
}

// withinWorkspace reports whether abs is inside the workspace.
func (c *Context) withinWorkspace(abs string) bool {
	rel, err := filepath.Rel(c.Workspace, abs)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Rel converts an absolute path to a workspace-relative slash path.
func (c *Context) Rel(abs string) string {
	rel, err := filepath.Rel(c.Workspace, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(rel)
}

// Resolve exposes resolvePath to other packages (used by plugins).
func (c *Context) Resolve(rel string) (string, error) { return c.resolvePath(rel) }
