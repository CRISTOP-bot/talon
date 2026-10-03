// Package plugin loads Talon plugins. A plugin is a separate executable that
// speaks newline-delimited JSON-RPC 2.0 over stdin/stdout, which means plugins
// are language-agnostic and can be distributed independently of Talon.
//
// Manifest (talon-plugin.json):
//
//	{
//	  "name": "docker",
//	  "version": "1.0.0",
//	  "description": "Docker helpers",
//	  "command": "./docker-plugin",
//	  "args": [],
//	  "tool_prefix": "docker"
//	}
package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/talon-cli/talon/internal/errs"
	"github.com/talon-cli/talon/internal/logger"
	"github.com/talon-cli/talon/internal/perm"
	"github.com/talon-cli/talon/internal/rpc"
	"github.com/talon-cli/talon/internal/tools"
)

// ManifestName is the file that describes a plugin directory.
const ManifestName = "talon-plugin.json"

// Manifest is the parsed plugin manifest.
type Manifest struct {
	Name        string            `json:"name"`
	Version     string            `json:"version"`
	Description string            `json:"description"`
	Command     string            `json:"command"`
	Args        []string          `json:"args"`
	Env         map[string]string `json:"env"`
	ToolPrefix  string            `json:"tool_prefix"`
	// Risk overrides the default risk of every tool the plugin exposes.
	Risk string `json:"risk"`
}

// Plugin is a running plugin process.
type Plugin struct {
	Manifest Manifest
	// Dir is the plugin directory (used for relative commands).
	Dir string

	cmd    *exec.Cmd
	client *rpc.Client
	stderr *lineTail

	mu      sync.Mutex
	stopped bool
	tools   []ToolInfo
}

// ToolInfo is a tool exposed by a plugin.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	// InputSchemaAlt is the snake_case spelling used by MCP servers.
	InputSchemaAlt map[string]any `json:"input_schema"`
}

// Schema returns whichever input schema field the peer used.
func (t ToolInfo) Schema() map[string]any {
	if t.InputSchema != nil {
		return t.InputSchema
	}
	return t.InputSchemaAlt
}

// ProtocolVersion is the version Talon implements for plugins.
const ProtocolVersion = 1

type initializeResult struct {
	Protocol int    `json:"protocol"`
	Name     string `json:"name"`
	Version  string `json:"version"`
	Tools    []struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		InputSchema map[string]any `json:"inputSchema"`
		InputAlt    map[string]any `json:"input_schema"`
	} `json:"tools"`
}

// Install starts the plugin process and performs the handshake.
func Install(ctx context.Context, dir string, log *logger.Logger) (*Plugin, error) {
	manifest, err := LoadManifest(dir)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(manifest.Command) == "" {
		return nil, errs.Config("plugin", "%s has no command", filepath.Join(dir, ManifestName))
	}
	command := manifest.Command
	if !filepath.IsAbs(command) {
		command = filepath.Join(dir, command)
	}
	if _, statErr := os.Stat(command); statErr != nil {
		if _, lookErr := exec.LookPath(manifest.Command); lookErr != nil {
			return nil, errs.NotFound("plugin", "plugin %q: executable %s not found", manifest.Name, manifest.Command)
		}
		command = manifest.Command
	}

	cmd := exec.CommandContext(ctx, command, manifest.Args...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	for k, v := range manifest.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errs.Internal("plugin", "%s", err.Error())
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errs.Internal("plugin", "%s", err.Error())
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, errs.Internal("plugin", "%s", err.Error())
	}
	if err := cmd.Start(); err != nil {
		return nil, errs.Internal("plugin", "cannot start %s: %v", manifest.Name, err)
	}

	tail := newLineTail(40)
	go tail.consume(stderrPipe)

	p := &Plugin{Manifest: manifest, Dir: dir, cmd: cmd, stderr: tail}
	p.client = rpc.NewClient(stdinReadWriter{Reader: stdout, Writer: stdin}, nil)
	p.client.CallTimeout = 60 * time.Second
	p.client.OnLog = func(format string, args ...any) { log.Debugf("plugin "+manifest.Name+": "+format, args...) }

	var res initializeResult
	err = p.client.Call(ctx, "initialize", map[string]any{
		"protocol":     ProtocolVersion,
		"client":       "talon",
		"pluginName":   manifest.Name,
		"capabilities": map[string]any{"tools": true},
	}, &res)
	if err != nil {
		_ = p.Stop()
		return nil, errs.Newf(errs.KindNetwork, "plugin",
			"plugin %q did not answer the handshake: %v", manifest.Name, err)
	}
	if res.Protocol != 0 && res.Protocol != ProtocolVersion {
		_ = p.Stop()
		return nil, errs.Config("plugin",
			"plugin %q speaks protocol %d, this Talon implements %d", manifest.Name, res.Protocol, ProtocolVersion)
	}
	// Plugins may declare their tools in the handshake; otherwise ask for them.
	if len(res.Tools) > 0 {
		for _, t := range res.Tools {
			p.tools = append(p.tools, ToolInfo{
				Name: t.Name, Description: t.Description,
				InputSchema: t.InputSchema, InputSchemaAlt: t.InputAlt,
			})
		}
	} else {
		listed, lerr := p.ListTools(ctx)
		if lerr != nil {
			_ = p.Stop()
			return nil, lerr
		}
		p.tools = listed
	}
	sort.Slice(p.tools, func(i, j int) bool { return p.tools[i].Name < p.tools[j].Name })
	return p, nil
}

// stdinReadWriter joins a process's stdout and stdin into one stream.
type stdinReadWriter struct {
	io.Reader
	io.Writer
}

// ListTools asks the plugin for its tools.
func (p *Plugin) ListTools(ctx context.Context) ([]ToolInfo, error) {
	var res struct {
		Tools []ToolInfo `json:"tools"`
	}
	if err := p.client.Call(ctx, "tools/list", map[string]any{}, &res); err != nil {
		return nil, errs.Newf(errs.KindNetwork, "plugin", "plugin %q: tools/list failed: %v", p.Manifest.Name, err)
	}
	return res.Tools, nil
}

// Tools returns the tools discovered at install time.
func (p *Plugin) Tools() []ToolInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]ToolInfo(nil), p.tools...)
}

// Stderr returns the last lines the plugin wrote to stderr, used in errors.
func (p *Plugin) Stderr() string {
	if p.stderr == nil {
		return ""
	}
	return p.stderr.String()
}

// CallTool invokes a plugin tool and returns its textual result.
func (p *Plugin) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if args == nil {
		args = json.RawMessage("{}")
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		// Plugins may spell the flag either way; accept both.
		IsError    bool `json:"is_error"`
		IsErrorAlt bool `json:"isError"`
	}
	err := p.client.Call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	}, &res)
	if err != nil {
		msg := err.Error()
		if tail := p.Stderr(); tail != "" {
			msg += "\n" + tail
		}
		return "", errs.Newf(errs.KindExecution, "plugin", "%s", msg)
	}
	var parts []string
	for _, c := range res.Content {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if res.IsError || res.IsErrorAlt {
		return text, errs.Newf(errs.KindExecution, "plugin", "error: %s", text)
	}
	return text, nil
}

// Stop terminates the plugin process.
func (p *Plugin) Stop() error {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return nil
	}
	p.stopped = true
	p.mu.Unlock()
	if p.client != nil {
		_ = p.client.Notify("shutdown", map[string]any{})
	}
	if p.cmd != nil && p.cmd.Process != nil {
		done := make(chan error, 1)
		go func() { done <- p.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = p.cmd.Process.Kill()
			<-done
		}
	}
	if p.client != nil {
		p.client.Close()
	}
	return nil
}

// QualifiedName prefixes a plugin tool name.
func (p *Plugin) QualifiedName(tool string) string {
	prefix := p.Manifest.ToolPrefix
	if prefix == "" {
		prefix = p.Manifest.Name
	}
	return prefix + "_" + strings.ReplaceAll(tool, "-", "_")
}

// Definitions converts the plugin's tools into Talon tool definitions.
func (p *Plugin) Definitions(ctx *tools.Context) []*tools.Definition {
	risk := riskOf(p.Manifest.Risk)
	var out []*tools.Definition
	for _, t := range p.Tools() {
		tool := t
		name := p.QualifiedName(tool.Name)
		out = append(out, &tools.Definition{
			Name:        name,
			Description: withPrefix(tool.Description, p.Manifest.Name),
			Parameters:  tool.Schema(),
			Risk:        risk,
			Source:      "plugin:" + p.Manifest.Name,
			Handler: func(c *tools.Context, args json.RawMessage) (tools.Result, error) {
				text, err := p.CallTool(c.Ctx, tool.Name, args)
				if err != nil {
					return tools.Result{}, err
				}
				return tools.Result{
					Content: text,
					Display: name,
				}, nil
			},
			Summarize: func(args json.RawMessage) string {
				return fmt.Sprintf("Run plugin tool %s", name)
			},
		})
	}
	_ = ctx
	return out
}

func withPrefix(desc, plugin string) string {
	if desc == "" {
		return "Tool provided by the " + plugin + " plugin."
	}
	return desc
}

func riskOf(name string) perm.Risk {
	switch strings.ToLower(name) {
	case "read", "read-only":
		return perm.RiskRead
	case "write":
		return perm.RiskWrite
	case "exec":
		return perm.RiskExec
	case "danger", "dangerous":
		return perm.RiskDanger
	default:
		return perm.RiskExec
	}
}

// LoadManifest reads talon-plugin.json from a directory.
func LoadManifest(dir string) (Manifest, error) {
	path := filepath.Join(dir, ManifestName)
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, errs.NotFound("plugin", "no %s in %s", ManifestName, dir)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, errs.Parse("plugin", "%s is not valid JSON: %v", path, err)
	}
	if m.Name == "" {
		return Manifest{}, errs.Config("plugin", "%s has no name", path)
	}
	return m, nil
}

// Discover lists the plugin directories found in a plugin root.
func Discover(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, errs.Wrap(errs.KindPermission, "plugin", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), ManifestName)); err != nil {
			continue
		}
		out = append(out, filepath.Join(root, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

// lineTail keeps the last N lines written to a stream.
type lineTail struct {
	mu    sync.Mutex
	lines []string
	limit int
	done  chan struct{}
}

func newLineTail(limit int) *lineTail {
	return &lineTail{limit: limit, done: make(chan struct{})}
}

func (l *lineTail) consume(r io.Reader) {
	defer close(l.done)
	buf := make([]byte, 4096)
	var pending []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending = append(pending, buf[:n]...)
			for {
				idx := strings.IndexByte(string(pending), '\n')
				if idx < 0 {
					break
				}
				line := string(pending[:idx])
				pending = pending[idx+1:]
				l.append(line)
			}
		}
		if err != nil {
			if len(pending) > 0 {
				l.append(string(pending))
			}
			return
		}
	}
}

func (l *lineTail) append(line string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
	if len(l.lines) > l.limit {
		l.lines = l.lines[len(l.lines)-l.limit:]
	}
}

func (l *lineTail) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.lines) == 0 {
		return ""
	}
	return strings.Join(l.lines, "\n")
}
