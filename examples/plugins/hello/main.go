// Command hello is an example Talon plugin.
//
// A plugin is a normal executable that speaks newline-delimited JSON-RPC 2.0 on
// stdin/stdout. Build it with:
//
//	go build -o hello .
//
// then install it with:
//
//	talon plugins install .
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type toolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func main() {
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	out := json.NewEncoder(os.Stdout)

	for in.Scan() {
		var req request
		if err := json.Unmarshal(in.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			reply(out, req.ID, map[string]any{
				"protocol": 1,
				"name":     "hello",
				"version":  "1.0.0",
			}, nil)
		case "tools/list":
			reply(out, req.ID, map[string]any{"tools": []toolSpec{
				{
					Name: "greet",
					Description: "Greet someone. Use it when the user wants a greeting or " +
						"when a friendly message is useful.",
					InputSchema: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{
								"type":        "string",
								"description": "Who to greet",
							},
						},
						"required": []string{"name"},
					},
				},
				{
					Name:        "now",
					Description: "Return the current server time in RFC3339.",
					InputSchema: map[string]any{
						"type":       "object",
						"properties": map[string]any{},
						"required":   []string{},
					},
				},
			}}, nil)
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(req.Params, &params); err != nil {
				replyError(out, req.ID, -32602, "invalid params")
				continue
			}
			text, isErr := call(params.Name, params.Arguments)
			reply(out, req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": isErr,
			}, nil)
		case "shutdown":
			return
		}
	}
}

func call(name string, args map[string]any) (string, bool) {
	switch name {
	case "greet":
		who, _ := args["name"].(string)
		if strings.TrimSpace(who) == "" {
			return "error: the name argument is required", true
		}
		hour := time.Now().Hour()
		part := "Good evening"
		switch {
		case hour < 12:
			part = "Good morning"
		case hour < 18:
			part = "Good afternoon"
		}
		return fmt.Sprintf("%s, %s!", part, who), false
	case "now":
		return time.Now().Format(time.RFC3339), false
	default:
		return "error: unknown tool " + name, true
	}
}

func reply(out *json.Encoder, id json.RawMessage, result any, rpcErr *rpcError) {
	if id == nil {
		return
	}
	msg := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(id)}
	if rpcErr != nil {
		msg["error"] = rpcErr
	} else {
		msg["result"] = result
	}
	_ = out.Encode(msg)
}

func replyError(out *json.Encoder, id json.RawMessage, code int, message string) {
	reply(out, id, nil, &rpcError{Code: code, Message: message})
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
