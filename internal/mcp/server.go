// Package mcp implements the server side of the Model Context Protocol.
//
// Talon can already be an MCP *client* (internal/mcp connects to servers the user
// configures). This file is the other half: exposing Talon's own tools over MCP
// so another agent — or a desktop assistant — can read and search a project
// through the same code paths, with the same gates, that Talon uses itself.
//
// Only the read-only tools are exposed. A server that could write files or run
// commands would hand those powers to every client that connects, which is not a
// trade this project should make silently.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/CRISTOP-bot/talon/internal/llm"
	"github.com/CRISTOP-bot/talon/internal/perm"
	"github.com/CRISTOP-bot/talon/internal/tools"
)

// ServerProtocolVersion is the MCP revision this server implements. It is the
// same constant the client negotiates with.
const ServerProtocolVersion = ProtocolVersion

// ServerName and ServerVersion identify Talon to clients.
const (
	ServerName    = "talon"
	ServerVersion = "1"
)

// jsonrpcRequest is one JSON-RPC 2.0 message. A request has an id; a
// notification does not.
type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// jsonrpcError is the error member of a JSON-RPC response.
type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// jsonrpcResponse is the reply to a request.
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

// JSON-RPC error codes used by this server.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Serve runs the MCP server over a pair of streams until the input closes.
func Serve(ctx context.Context, in io.Reader, out io.Writer, registry *tools.Registry, toolCtx *tools.Context) error {
	reader := bufio.NewReaderSize(in, 64*1024)
	writer := bufio.NewWriter(out)
	s := &server{registry: registry, toolCtx: toolCtx}

	for {
		if ctx.Err() != nil {
			return nil
		}
		line, err := readLine(reader)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		var req jsonrpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			if werr := s.writeError(writer, nil, codeParseError, "invalid JSON: "+err.Error()); werr != nil {
				return werr
			}
			continue
		}
		if len(req.ID) == 0 {
			// A notification expects no reply; nothing to do but honour it.
			continue
		}
		resp := s.handle(&req)
		if err := writeJSON(writer, resp); err != nil {
			return err
		}
	}
}

// server holds the tool registry for one connection.
type server struct {
	registry *tools.Registry
	toolCtx  *tools.Context
	mu       sync.Mutex
}

// handle dispatches one request.
func (s *server) handle(req *jsonrpcRequest) jsonrpcResponse {
	switch req.Method {
	case "initialize":
		return s.ok(req, map[string]any{
			"protocolVersion": ServerProtocolVersion,
			"capabilities": map[string]any{
				"tools": map[string]any{"listChanged": false},
			},
			"serverInfo": map[string]any{"name": ServerName, "version": ServerVersion},
		})
	case "ping":
		return s.ok(req, map[string]any{})
	case "tools/list":
		return s.ok(req, map[string]any{"tools": s.toolList()})
	case "tools/call":
		return s.callTool(req)
	default:
		return s.err(req, codeMethodNotFound, "unknown method "+req.Method)
	}
}

// toolList describes the exposed tools. Only read-only tools are advertised:
// a client that connects to this server should not be handed the power to
// change files, no matter how it is configured locally.
func (s *server) toolList() []map[string]any {
	out := []map[string]any{}
	for _, def := range s.registry.All() {
		if def.Risk != perm.RiskRead {
			continue
		}
		spec := def.Spec()
		desc := spec.Description
		if risk := def.Risk.String(); risk != "" {
			desc += " (risk: " + risk + ")"
		}
		out = append(out, map[string]any{
			"name":        def.Name,
			"description": desc,
			"inputSchema": spec.Parameters,
		})
	}
	return out
}

// callTool runs one tool and wraps the result in the MCP content shape.
func (s *server) callTool(req *jsonrpcRequest) jsonrpcResponse {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return s.err(req, codeInvalidParams, "invalid params: "+err.Error())
	}
	def, ok := s.registry.Get(params.Name)
	if !ok {
		return s.err(req, codeInvalidParams, "unknown tool "+params.Name)
	}
	args := params.Arguments
	if len(args) == 0 {
		args = json.RawMessage(`{}`)
	}
	// The permission decision is made here, not by the caller: an MCP client is
	// not Talon's agent loop and would otherwise skip the approval step entirely.
	if s.toolCtx.Policy != nil {
		decision, reason := s.toolCtx.Policy.Decision(def.Request(args))
		if decision != perm.Allow {
			return s.ok(req, map[string]any{
				"isError": true,
				"content": []map[string]any{{"type": "text",
					"text": "refused by the permission policy: " + reason}},
			})
		}
	}
	result, err := def.Run(s.toolCtx, args)
	if err != nil {
		// A tool failure is reported as content, not as a protocol error: the
		// client asked a reasonable question and deserves the reason.
		return s.ok(req, map[string]any{
			"isError": true,
			"content": []map[string]any{{"type": "text", "text": err.Error()}},
		})
	}
	return s.ok(req, map[string]any{
		"isError": false,
		"content": []map[string]any{{"type": "text", "text": result.Content}},
	})
}

func (s *server) ok(req *jsonrpcRequest, result any) jsonrpcResponse {
	return jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
}

func (s *server) err(req *jsonrpcRequest, code int, msg string) jsonrpcResponse {
	return jsonrpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &jsonrpcError{Code: code, Message: msg}}
}

// writeError sends an error response for a message that could not be parsed.
func (s *server) writeError(w io.Writer, id json.RawMessage, code int, msg string) error {
	return writeJSON(w, jsonrpcResponse{JSONRPC: "2.0", ID: id, Error: &jsonrpcError{Code: code, Message: msg}})
}

func writeJSON(w io.Writer, resp jsonrpcResponse) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("encoding the response: %w", err)
	}
	if _, err := w.Write(append(data, '\n')); err != nil {
		return err
	}
	if f, ok := w.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// readLine reads one newline-delimited message, tolerating long lines.
func readLine(r *bufio.Reader) (string, error) {
	var b strings.Builder
	for {
		chunk, isPrefix, err := r.ReadLine()
		if err != nil {
			if b.Len() == 0 {
				return "", err
			}
			return b.String(), nil
		}
		b.Write(chunk)
		if !isPrefix {
			return b.String(), nil
		}
	}
}

// ToolSpecs exposes the registry descriptions, used by the command to report
// what the server offers before it starts.
func ToolSpecs(registry *tools.Registry) []llm.ToolSpec {
	return registry.Specs()
}
