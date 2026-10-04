package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CRISTOP-bot/talon/internal/perm"
	"github.com/CRISTOP-bot/talon/internal/tools"
)

// newServer builds a registry and context rooted at a temporary project.
func newServer(t *testing.T, level perm.Level) (*tools.Registry, *tools.Context, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".talon"), 0o755); err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry()
	reg.Register(tools.All()...)
	limits := tools.DefaultLimits()
	ctx := &tools.Context{
		Ctx:       context.Background(),
		Workspace: dir,
		Limits:    limits,
		Policy:    perm.New(perm.Config{Level: level, Workspace: dir}),
	}
	return reg, ctx, dir
}

// run feeds requests to the server and returns the decoded responses.
func run(t *testing.T, reg *tools.Registry, ctx *tools.Context, requests ...string) []map[string]any {
	t.Helper()
	in := strings.NewReader(strings.Join(requests, "\n") + "\n")
	var out bytes.Buffer
	if err := Serve(context.Background(), in, &out, reg, ctx); err != nil {
		t.Fatalf("Serve returned an error: %v", err)
	}
	var out2 []map[string]any
	for _, line := range bytes.Split(out.Bytes(), []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("response is not JSON: %q", line)
		}
		out2 = append(out2, m)
	}
	return out2
}

func TestInitializeHandshake(t *testing.T) {
	reg, ctx, _ := newServer(t, perm.ReadOnly)
	got := run(t, reg, ctx,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	if len(got) != 1 {
		t.Fatalf("expected one response, got %d", len(got))
	}
	result, ok := got[0]["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize returned no result: %v", got[0])
	}
	if result["protocolVersion"] != ProtocolVersion {
		t.Errorf("protocol version = %v, want %s", result["protocolVersion"], ProtocolVersion)
	}
	info, _ := result["serverInfo"].(map[string]any)
	if info["name"] != ServerName {
		t.Errorf("server name = %v, want %s", info["name"], ServerName)
	}
	if _, ok := result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Error("the server does not advertise tool support")
	}
}

// TestOnlyReadToolsAreAdvertised proves a connecting client is not handed write
// or execute powers.
func TestOnlyReadToolsAreAdvertised(t *testing.T) {
	reg, ctx, _ := newServer(t, perm.FullAccess)
	got := run(t, reg, ctx, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	list, _ := got[0]["result"].(map[string]any)["tools"].([]any)
	if len(list) == 0 {
		t.Fatal("no tools were advertised")
	}
	for _, item := range list {
		tool, _ := item.(map[string]any)
		name, _ := tool["name"].(string)
		def, _ := reg.Get(name)
		if def == nil {
			t.Fatalf("advertised an unknown tool %q", name)
		}
		if def.Risk != perm.RiskRead {
			t.Errorf("%s has risk %v and must not be exposed over MCP", name, def.Risk)
		}
	}
}

func TestToolsListDescribesSchemas(t *testing.T) {
	reg, ctx, _ := newServer(t, perm.ReadOnly)
	got := run(t, reg, ctx, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	list, _ := got[0]["result"].(map[string]any)["tools"].([]any)
	if len(list) == 0 {
		t.Fatal("no tools were advertised")
	}
	found := false
	for _, item := range list {
		tool, _ := item.(map[string]any)
		if tool["name"] == "read_file" {
			found = true
			if tool["description"] == "" {
				t.Error("read_file has no description")
			}
			if tool["inputSchema"] == nil {
				t.Error("read_file has no input schema")
			}
		}
	}
	if !found {
		t.Error("read_file is missing from the tool list")
	}
}

func TestToolsCallReturnsContent(t *testing.T) {
	reg, ctx, _ := newServer(t, perm.ReadOnly)
	got := run(t, reg, ctx,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"main.go"}}}`)
	result, _ := got[0]["result"].(map[string]any)
	if result == nil {
		t.Fatalf("tools/call returned no result: %v", got[0])
	}
	if result["isError"] == true {
		t.Fatalf("read_file failed: %v", result)
	}
	content, _ := result["content"].([]any)
	if len(content) == 0 {
		t.Fatal("no content was returned")
	}
	first, _ := content[0].(map[string]any)
	if first["type"] != "text" {
		t.Errorf("content type = %v, want text", first["type"])
	}
	if text, _ := first["text"].(string); !strings.Contains(text, "package main") {
		t.Errorf("unexpected content: %q", text)
	}
}

// TestWritesAreRefused proves the server does not hand write powers to every
// client that connects.
func TestWritesAreRefused(t *testing.T) {
	reg, ctx, dir := newServer(t, perm.ReadOnly)
	got := run(t, reg, ctx,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"write_file","arguments":{"path":"nuevo.txt","content":"x"}}}`)
	result, _ := got[0]["result"].(map[string]any)
	if result == nil {
		t.Fatalf("expected a result carrying the refusal: %v", got[0])
	}
	if result["isError"] != true {
		t.Fatalf("write_file should have been refused in read-only mode: %v", result)
	}
	if _, err := os.Stat(filepath.Join(dir, "nuevo.txt")); err == nil {
		t.Fatal("the file was created despite the refusal")
	}
}

func TestUnknownToolIsAnInvalidParamsError(t *testing.T) {
	reg, ctx, _ := newServer(t, perm.ReadOnly)
	got := run(t, reg, ctx,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"no_such_tool","arguments":{}}}`)
	if _, isResult := got[0]["result"]; isResult {
		t.Fatalf("an unknown tool must be an error, not a result: %v", got[0])
	}
	errObj, _ := got[0]["error"].(map[string]any)
	if errObj["code"].(float64) != codeInvalidParams {
		t.Errorf("error code = %v, want %d", errObj["code"], codeInvalidParams)
	}
}

func TestUnknownMethodIsRejected(t *testing.T) {
	reg, ctx, _ := newServer(t, perm.ReadOnly)
	got := run(t, reg, ctx, `{"jsonrpc":"2.0","id":1,"method":"does/not/exist"}`)
	errObj, _ := got[0]["error"].(map[string]any)
	if errObj == nil || errObj["code"].(float64) != codeMethodNotFound {
		t.Fatalf("expected method-not-found, got %v", got[0])
	}
}

func TestMalformedJSONDoesNotKillTheServer(t *testing.T) {
	reg, ctx, _ := newServer(t, perm.ReadOnly)
	got := run(t, reg, ctx,
		`{"jsonrpc":"2.0","id":1,"method":"ping"}`,
		`{not json`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if len(got) != 3 {
		t.Fatalf("expected three responses (two pings and one parse error), got %d: %v", len(got), got)
	}
	if got[1]["error"] == nil {
		t.Error("the malformed line should produce a parse error")
	}
	if got[2]["result"] == nil {
		t.Error("the server did not survive the malformed line")
	}
}

func TestNotificationsGetNoReply(t *testing.T) {
	reg, ctx, _ := newServer(t, perm.ReadOnly)
	got := run(t, reg, ctx, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	if len(got) != 0 {
		t.Fatalf("a notification must not be answered, got %v", got)
	}
}

func TestResponsesCarryTheRequestID(t *testing.T) {
	reg, ctx, _ := newServer(t, perm.ReadOnly)
	got := run(t, reg, ctx,
		`{"jsonrpc":"2.0","id":"abc","method":"ping"}`,
		`{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	if got[0]["id"] != "abc" {
		t.Errorf("id = %v, want abc", got[0]["id"])
	}
	if got[1]["id"].(float64) != 7 {
		t.Errorf("id = %v, want 7", got[1]["id"])
	}
}

func TestLongLineIsNotTruncated(t *testing.T) {
	reg, ctx, _ := newServer(t, perm.ReadOnly)
	// A payload well past the reader's buffer must survive.
	big := strings.Repeat("x", 200000)
	req := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"` + big + `"}}}`
	got := run(t, reg, ctx, req)
	if len(got) != 1 {
		t.Fatalf("a long request should still get one response, got %d", len(got))
	}
	result, _ := got[0]["result"].(map[string]any)
	if result == nil {
		t.Fatalf("the long request was not answered: %v", got[0])
	}
}
