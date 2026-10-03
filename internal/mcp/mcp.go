// Package mcp is a client for Model Context Protocol servers. Talon speaks the
// stdio transport, so an MCP server is just another tool provider: the tools it
// advertises become Talon tools prefixed with the server name.
//
// Only the tool surface is implemented (initialize, tools/list, tools/call);
// that is what the spec asks a client for.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/talon-cli/talon/internal/errs"
	"github.com/talon-cli/talon/internal/logger"
	"github.com/talon-cli/talon/internal/perm"
	"github.com/talon-cli/talon/internal/rpc"
	"github.com/talon-cli/talon/internal/tools"
)

// ProtocolVersion is the MCP revision Talon implements.
const ProtocolVersion = "2024-11-05"

// ClientInfo is how Talon identifies itself during the handshake.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Server is a connected MCP server.
type Server struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	// ToolPrefix overrides the default prefix (the server name).
	ToolPrefix string

	cmd    *exec.Cmd
	client *rpc.Client

	mu      sync.Mutex
	stopped bool
	tools   []Tool
}

// Tool is a tool advertised by an MCP server.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Spec describes an MCP server to connect to. It is a value type so callers do
// not have to think about copying the running Server's mutex.
type Spec struct {
	Name       string
	Command    string
	Args       []string
	Env        map[string]string
	ToolPrefix string
}

// Connect starts the server process and completes the MCP handshake.
func Connect(ctx context.Context, spec Spec, log *logger.Logger) (*Server, error) {
	if spec.ToolPrefix == "" {
		spec.ToolPrefix = spec.Name
	}
	cmd := exec.CommandContext(ctx, spec.Command, spec.Args...)
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, errs.Internal("mcp", "%s", err.Error())
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errs.Internal("mcp", "%s", err.Error())
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, errs.NotFound("mcp", "cannot start MCP server %q: %v", spec.Name, err)
	}

	s := &Server{Name: spec.Name, Command: spec.Command, Args: spec.Args, Env: spec.Env,
		ToolPrefix: spec.ToolPrefix, cmd: cmd}
	s.client = rpc.NewClient(readWriter{Reader: stdout, Writer: stdin}, nil)
	s.client.CallTimeout = 60 * time.Second
	s.client.OnLog = func(format string, args ...any) { log.Debugf("mcp "+spec.Name+": "+format, args...) }

	var initRes struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools map[string]any `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"serverInfo"`
	}
	err = s.client.Call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"clientInfo":      ClientInfo{Name: "talon", Version: "dev"},
	}, &initRes)
	if err != nil {
		_ = s.Stop()
		return nil, errs.Newf(errs.KindNetwork, "mcp", "server %q failed to initialize: %v", spec.Name, err)
	}
	if err := s.client.Notify("notifications/initialized", map[string]any{}); err != nil {
		log.Debugf("mcp %s: initialized notification failed: %v", spec.Name, err)
	}
	tools, err := s.ListTools(ctx)
	if err != nil {
		_ = s.Stop()
		return nil, err
	}
	s.tools = tools
	return s, nil
}

type readWriter struct {
	io.Reader
	io.Writer
}

// ListTools asks the server which tools it provides.
func (s *Server) ListTools(ctx context.Context) ([]Tool, error) {
	var res struct {
		Tools []Tool `json:"tools"`
	}
	if err := s.client.Call(ctx, "tools/list", map[string]any{}, &res); err != nil {
		return nil, errs.Newf(errs.KindNetwork, "mcp", "server %q: tools/list failed: %v", s.Name, err)
	}
	return res.Tools, nil
}

// CallTool runs a tool and returns the text parts of the result.
func (s *Server) CallTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if args == nil {
		args = json.RawMessage("{}")
	}
	var res struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	err := s.client.Call(ctx, "tools/call", map[string]any{
		"name":      name,
		"arguments": args,
	}, &res)
	if err != nil {
		return "", errs.Newf(errs.KindExecution, "mcp", "%s", err)
	}
	var parts []string
	for _, c := range res.Content {
		if c.Text != "" {
			parts = append(parts, c.Text)
		}
	}
	text := strings.Join(parts, "\n")
	if res.IsError {
		return text, errs.Newf(errs.KindExecution, "mcp", "error: %s", text)
	}
	return text, nil
}

// Stop terminates the server process.
func (s *Server) Stop() error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return nil
	}
	s.stopped = true
	s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		done := make(chan error, 1)
		go func() { done <- s.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = s.cmd.Process.Kill()
			<-done
		}
	}
	if s.client != nil {
		s.client.Close()
	}
	return nil
}

// QualifiedName prefixes a tool with the server name.
func (s *Server) QualifiedName(tool string) string {
	return s.ToolPrefix + "_" + strings.ReplaceAll(tool, "-", "_")
}

// Definitions converts the server's tools into Talon tool definitions. MCP tools
// may do anything, so they default to the exec risk level.
func (s *Server) Definitions() []*tools.Definition {
	var out []*tools.Definition
	for _, t := range s.tools {
		tool := t
		name := s.QualifiedName(tool.Name)
		schema := tool.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, &tools.Definition{
			Name:        name,
			Description: orDefault(tool.Description, "Tool provided by the MCP server "+s.Name+"."),
			Parameters:  schema,
			Risk:        perm.RiskExec,
			Source:      "mcp:" + s.Name,
			Handler: func(ctx *tools.Context, args json.RawMessage) (tools.Result, error) {
				text, err := s.CallTool(ctx.Ctx, tool.Name, args)
				if err != nil {
					return tools.Result{}, err
				}
				return tools.Result{Content: text, Display: name}, nil
			},
			Summarize: func(args json.RawMessage) string {
				return fmt.Sprintf("Call MCP tool %s on %s", tool.Name, s.Name)
			},
		})
	}
	return out
}

func orDefault(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}
