package plugin

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
	"github.com/talon-cli/talon/internal/tools"
)

// fakePlugin is a Go program compiled on the fly that speaks the plugin
// protocol. Tests therefore exercise a real process and a real pipe.
const fakePluginSource = `package main

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
			reply(out, req.ID, map[string]any{"protocol": 1, "name": "fake", "version": "9.9"})
		case "tools/list":
			reply(out, req.ID, map[string]any{"tools": []map[string]any{{
				"name":        "echo",
				"description": "Echo a message back.",
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{"text": map[string]any{"type": "string"}},
					"required":   []string{"text"},
				},
			}}})
		case "tools/call":
			var p struct {
				Name string         ` + "`json:\"name\"`" + `
				Args map[string]any ` + "`json:\"arguments\"`" + `
			}
			_ = json.Unmarshal(req.Params, &p)
			if p.Name == "boom" {
				reply(out, req.ID, map[string]any{
					"content": []map[string]any{{"type": "text", "text": "plugin exploded"}},
					"isError": true,
				})
				continue
			}
			text, _ := p.Args["text"].(string)
			reply(out, req.ID, map[string]any{
				"content": []map[string]any{{"type": "text", "text": "echo: " + text}},
			})
		case "shutdown":
			return
		}
	}
}

func reply(out *json.Encoder, id json.RawMessage, result any) {
	if id == nil {
		return
	}
	_ = out.Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}
`

// buildFakePlugin compiles the fake plugin into a plugin directory and returns it.
func buildFakePlugin(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	goMod := "module fake\n\ngo 1.24\n"
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.go"), []byte(fakePluginSource), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "fake-plugin")
	if filepath.Ext(bin) == "" {
		bin += ".bin"
	}
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = src
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the fake plugin: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func defaultManifest(name, command, risk string) string {
	return `{
  "name": "` + name + `",
  "version": "1.2.3",
  "description": "fake plugin for tests",
  "command": "` + command + `",
  "tool_prefix": "` + name + `",
  "risk": "` + risk + `"
}`
}

func TestInstallHandshakeAndToolList(t *testing.T) {
	dir := buildFakePlugin(t, defaultManifest("fake", "./fake-plugin.bin", "read"))
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	p, err := Install(ctx, dir, logger.Discard)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	defer p.Stop()

	if p.Manifest.Name != "fake" || p.Manifest.Version != "1.2.3" {
		t.Errorf("manifest = %+v", p.Manifest)
	}
	toolsList := p.Tools()
	if len(toolsList) != 1 || toolsList[0].Name != "echo" {
		t.Fatalf("tools = %+v", toolsList)
	}
	if toolsList[0].Schema() == nil {
		t.Error("input schema missing")
	}
}

func TestCallTool(t *testing.T) {
	dir := buildFakePlugin(t, defaultManifest("fake", "./fake-plugin.bin", "read"))
	p, err := Install(context.Background(), dir, logger.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	text, err := p.CallTool(context.Background(), "echo", json.RawMessage(`{"text":"hola"}`))
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	if text != "echo: hola" {
		t.Errorf("text = %q", text)
	}
}

func TestCallToolErrorIsReported(t *testing.T) {
	dir := buildFakePlugin(t, defaultManifest("fake", "./fake-plugin.bin", "read"))
	p, err := Install(context.Background(), dir, logger.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	_, err = p.CallTool(context.Background(), "boom", json.RawMessage(`{}`))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "plugin exploded") {
		t.Errorf("error = %v", err)
	}
}

func TestDefinitionsUseTheManifestRisk(t *testing.T) {
	dir := buildFakePlugin(t, defaultManifest("fake", "./fake-plugin.bin", "danger"))
	p, err := Install(context.Background(), dir, logger.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()

	defs := p.Definitions(nil)
	if len(defs) != 1 {
		t.Fatalf("definitions = %d", len(defs))
	}
	d := defs[0]
	if d.Name != "fake_echo" {
		t.Errorf("name = %q", d.Name)
	}
	if d.Risk != perm.RiskDanger {
		t.Errorf("risk = %v", d.Risk)
	}
	if d.Source != "plugin:fake" {
		t.Errorf("source = %q", d.Source)
	}
	if d.Parameters["type"] != "object" {
		t.Errorf("schema = %v", d.Parameters)
	}
	// The handler must forward to the plugin.
	res, err := d.Handler(&tools.Context{Ctx: context.Background()}, json.RawMessage(`{"text":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.Content != "echo: hi" {
		t.Errorf("content = %q", res.Content)
	}
}

func TestPermissionRequestUsesTheToolPrefix(t *testing.T) {
	dir := buildFakePlugin(t, defaultManifest("fake", "./fake-plugin.bin", "write"))
	p, err := Install(context.Background(), dir, logger.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Stop()
	d := p.Definitions(nil)[0]
	req := d.Request(json.RawMessage(`{"text":"hi"}`))
	if req.Tool != "fake_echo" {
		t.Errorf("tool = %q", req.Tool)
	}
	if req.Risk != perm.RiskWrite {
		t.Errorf("risk = %v", req.Risk)
	}
}

func TestInstallRejectsBadManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestName), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), dir, logger.Discard); err == nil {
		t.Error("expected an error for invalid JSON")
	}
	if _, err := LoadManifest(t.TempDir()); err == nil {
		t.Error("expected an error when the manifest is missing")
	}
}

func TestInstallRejectsMissingExecutable(t *testing.T) {
	dir := buildFakePlugin(t, defaultManifest("fake", "./does-not-exist", "read"))
	if _, err := Install(context.Background(), dir, logger.Discard); err == nil {
		t.Fatal("expected an error for a missing executable")
	}
}

func TestDiscover(t *testing.T) {
	root := t.TempDir()
	good := buildFakePlugin(t, defaultManifest("fake", "./fake-plugin.bin", "read"))
	data, err := os.ReadFile(filepath.Join(good, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "fake")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, ManifestName), data, 0o644); err != nil {
		t.Fatal(err)
	}
	// A directory without a manifest must be ignored.
	if err := os.MkdirAll(filepath.Join(root, "not-a-plugin"), 0o755); err != nil {
		t.Fatal(err)
	}
	dirs, err := Discover(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) != 1 || filepath.Base(dirs[0]) != "fake" {
		t.Errorf("discovered = %v", dirs)
	}
	if got, err := Discover(filepath.Join(root, "missing")); err != nil || got != nil {
		t.Errorf("a missing root should be empty, got %v %v", got, err)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	dir := buildFakePlugin(t, defaultManifest("fake", "./fake-plugin.bin", "read"))
	p, err := Install(context.Background(), dir, logger.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(); err != nil {
		t.Errorf("first Stop: %v", err)
	}
	if err := p.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestQualifiedNameSanitisesDashes(t *testing.T) {
	p := &Plugin{Manifest: Manifest{Name: "my-plugin"}}
	if got := p.QualifiedName("do-thing"); got != "my-plugin_do_thing" {
		t.Errorf("name = %q", got)
	}
}

func TestRiskOf(t *testing.T) {
	cases := map[string]perm.Risk{
		"read":      perm.RiskRead,
		"write":     perm.RiskWrite,
		"exec":      perm.RiskExec,
		"danger":    perm.RiskDanger,
		"unknown":   perm.RiskExec,
		"":          perm.RiskExec,
		"READ-ONLY": perm.RiskRead,
	}
	for in, want := range cases {
		if got := riskOf(in); got != want {
			t.Errorf("riskOf(%q) = %v, want %v", in, got, want)
		}
	}
}
