package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/talon-cli/talon/internal/logger"
	"github.com/talon-cli/talon/internal/perm"
)

// fakeServerSource is a minimal MCP server: it answers initialize, tools/list
// and tools/call over stdio.
const fakeServerSource = `package main

import (
	"bufio"
	"encoding/json"
	"os"
)

type request struct {
	JSONRPC string          ` + "`json:\"jsonrpc\"`" + `
	ID      json.RawMessage ` + "`json:\"id\"`" + `
	Method  string          ` + "`json:\"method\"`" + `
	Params  json.RawMessage ` + "`json:\"params,omitempty\"`" + `
}

func main() {
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 0, 65536), 4<<20)
	out := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			continue
		}
		switch req.Method {
		case "initialize":
			reply(out, req.ID, map[string]any{
				"protocolVersion": "2024-11-05",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "fake", "version": "1.0"},
			})
		case "tools/list":
			reply(out, req.ID, map[string]any{"tools": []map[string]any{{
				"name":        "list-dir",
				"description": "List a directory.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"path": map[string]any{"type": "string"}},
				},
			}}})
		case "tools/call":
			var p struct {
				Name string         ` + "`json:\"name\"`" + `
				Args map[string]any ` + "`json:\"arguments\"`" + `
			}
			_ = json.Unmarshal(req.Params, &p)
			path, _ := p.Args["path"].(string)
			if p.Name == "explode" {
				reply(out, req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "boom"}},
					"isError": true,
				})
				continue
			}
			entries, _ := os.ReadDir(path)
			names := []string{}
			for _, e := range entries {
				names = append(names, e.Name())
			}
			reply(out, req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": strings_Join(names, ",")}},
			})
		case "notifications/initialized":
			// no response expected
		}
	}
}

func strings_Join(items []string, sep string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}

func reply(out *json.Encoder, id json.RawMessage, result any) {
	if id == nil {
		return
	}
	_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
`

// buildServer compiles the fake MCP server and returns its path.
func buildServer(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fakeserver\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(fakeServerSource), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fake-mcp")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the fake MCP server: %v\n%s", err, out)
	}
	return bin
}

func TestConnectAndCallTool(t *testing.T) {
	bin := buildServer(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	srv, err := Connect(ctx, Spec{Name: "fs", Command: bin}, logger.Discard)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer srv.Stop()

	toolsList, err := srv.ListTools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(toolsList) != 1 || toolsList[0].Name != "list-dir" {
		t.Fatalf("tools = %+v", toolsList)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"path": dir})
	text, err := srv.CallTool(ctx, "list-dir", args)
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if !strings.Contains(text, "a.txt") {
		t.Errorf("text = %q", text)
	}
}

func TestToolErrorsAreSurfaced(t *testing.T) {
	bin := buildServer(t)
	srv, err := Connect(context.Background(), Spec{Name: "fs", Command: bin}, logger.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()
	_, err = srv.CallTool(context.Background(), "explode", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v", err)
	}
}

func TestDefinitionsArePrefixedAndRisky(t *testing.T) {
	bin := buildServer(t)
	srv, err := Connect(context.Background(), Spec{Name: "fs", Command: bin}, logger.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	defs := srv.Definitions()
	if len(defs) != 1 {
		t.Fatalf("definitions = %d", len(defs))
	}
	d := defs[0]
	if d.Name != "fs_list_dir" {
		t.Errorf("name = %q", d.Name)
	}
	if d.Risk != perm.RiskExec {
		t.Errorf("MCP tools must default to exec risk, got %v", d.Risk)
	}
	if d.Source != "mcp:fs" {
		t.Errorf("source = %q", d.Source)
	}
	if d.Parameters["type"] != "object" {
		t.Errorf("schema = %v", d.Parameters)
	}
}

func TestConnectFailsForMissingBinary(t *testing.T) {
	_, err := Connect(context.Background(), Spec{
		Name: "ghost", Command: filepath.Join(t.TempDir(), "does-not-exist"),
	}, logger.Discard)
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestToolPrefixOverride(t *testing.T) {
	srv := &Server{Name: "x", ToolPrefix: "custom"}
	if got := srv.QualifiedName("do-it"); got != "custom_do_it" {
		t.Errorf("name = %q", got)
	}
}
