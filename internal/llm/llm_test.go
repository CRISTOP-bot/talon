package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/talon-cli/talon/internal/errs"
)

func sse(w http.ResponseWriter, frames ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, f := range frames {
		fmt.Fprintf(w, "data: %s\n\n", f)
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func testOpts(base string) Options {
	return Options{
		APIKey:  "test-key",
		BaseURL: base,
		Model:   "test-model",
		Timeout: 5 * time.Second,
	}
}

func TestOpenAIStreamText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth header = %q", got)
		}
		sse(w,
			`{"model":"gpt-x","choices":[{"delta":{"content":"Hello"}}]}`,
			`{"choices":[{"delta":{"content":", world"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		)
	}))
	defer srv.Close()

	p := NewOpenAI(testOpts(srv.URL))
	resp, err := CollectStream(context.Background(), p, Request{
		Model:     "test-model",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		System:    "be brief",
		Tools:     []ToolSpec{{Name: "noop", Description: "does nothing", Parameters: JSONSchema(nil)}},
		MaxTokens: 100,
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Message.Content != "Hello, world" {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if resp.StopReason != StopEnd {
		t.Errorf("stop = %q", resp.StopReason)
	}
	if resp.Usage.TotalTokens != 15 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestOpenAIStreamToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		)
	}))
	defer srv.Close()

	p := NewOpenAI(testOpts(srv.URL))
	resp, err := CollectStream(context.Background(), p, Request{Model: "m"}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	call := resp.Message.ToolCalls[0]
	if call.Name != "read_file" || call.ID != "call_1" {
		t.Errorf("call = %+v", call)
	}
	var args map[string]string
	if err := call.ParsedArguments(&args); err != nil {
		t.Fatalf("arguments not valid JSON: %q", call.Arguments)
	}
	if args["path"] != "a.go" {
		t.Errorf("args = %v", args)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
}

func TestOpenAIRequestBodyShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		sse(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	req := Request{
		Model:       "m",
		System:      "system prompt",
		Messages:    []Message{{Role: RoleUser, Content: "hello"}},
		Tools:       []ToolSpec{{Name: "read_file", Description: "reads", Parameters: JSONSchema(Prop("string", "path"), "path")}},
		Temperature: 0.3,
		MaxTokens:   512,
	}
	if _, err := CollectStream(context.Background(), NewOpenAI(testOpts(srv.URL)), req, nil); err != nil {
		t.Fatal(err)
	}
	if got["model"] != "m" {
		t.Errorf("model = %v", got["model"])
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", msgs)
	}
	first := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "system prompt" {
		t.Errorf("system message = %v", first)
	}
	tools := got["tools"].([]any)
	tool := tools[0].(map[string]any)["function"].(map[string]any)
	if tool["name"] != "read_file" {
		t.Errorf("tool = %v", tool)
	}
	schema := tool["parameters"].(map[string]any)
	req2 := schema["required"].([]any)
	if len(req2) != 1 || req2[0] != "path" {
		t.Errorf("required = %v", req2)
	}
	if got["stream"] != true {
		t.Error("stream flag not set")
	}
	if got["max_tokens"].(float64) != 512 {
		t.Errorf("max_tokens = %v", got["max_tokens"])
	}
}

func TestOpenAIErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   errs.Kind
	}{
		{401, `{"error":{"message":"bad key"}}`, errs.KindAuth},
		{429, `{"error":{"message":"slow down"}}`, errs.KindRateLimit},
		{404, `{"error":{"message":"no such model"}}`, errs.KindNotFound},
		{400, `{"error":{"message":"bad request"}}`, errs.KindModel},
		{503, `{"error":{"message":"unavailable"}}`, errs.KindNetwork},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.status)
			fmt.Fprint(w, c.body)
		}))
		p := NewOpenAI(Options{APIKey: "k", BaseURL: srv.URL, Model: "m", MaxRetries: 0})
		_, err := CollectStream(context.Background(), p, Request{Model: "m"}, nil)
		if err == nil {
			t.Errorf("status %d: expected error", c.status)
			continue
		}
		if errs.KindOf(err) != c.want {
			t.Errorf("status %d: kind = %v, want %v (%v)", c.status, errs.KindOf(err), c.want, err)
		}
		if !strings.Contains(err.Error(), "provider returned an error") && !strings.Contains(err.Error(), "HTTP") {
			t.Errorf("status %d: unhelpful message %v", c.status, err)
		}
		srv.Close()
	}
}

func TestRetryOnRateLimit(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts < 3 {
			w.WriteHeader(429)
			fmt.Fprint(w, `{"error":{"message":"slow down"}}`)
			return
		}
		sse(w, `{"choices":[{"delta":{"content":"recovered"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()

	p := NewOpenAI(Options{APIKey: "k", BaseURL: srv.URL, Model: "m", MaxRetries: 3})
	resp, err := CollectStream(context.Background(), p, Request{Model: "m"}, nil)
	if err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if resp.Message.Content != "recovered" {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}

func TestConnectionRefusedIsNetworkError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	p := NewOpenAI(Options{APIKey: "k", BaseURL: url, Model: "m", MaxRetries: 0})
	_, err := CollectStream(context.Background(), p, Request{Model: "m"}, nil)
	if err == nil {
		t.Fatal("expected an error")
	}
	if errs.KindOf(err) != errs.KindNetwork && errs.KindOf(err) != errs.KindTimeout {
		t.Errorf("kind = %v", errs.KindOf(err))
	}
}

func TestContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	p := NewOpenAI(testOpts(srv.URL))
	_, err := CollectStream(ctx, p, Request{Model: "m"}, nil)
	if err == nil {
		t.Fatal("expected an error when the context expires")
	}
	if errs.KindOf(err) != errs.KindTimeout && errs.KindOf(err) != errs.KindCancelled && errs.KindOf(err) != errs.KindNetwork {
		t.Errorf("kind = %v (%v)", errs.KindOf(err), err)
	}
}

func TestMalformedStreamFrame(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices": not json`)
	}))
	defer srv.Close()
	p := NewOpenAI(testOpts(srv.URL))
	_, err := CollectStream(context.Background(), p, Request{Model: "m"}, nil)
	if err == nil {
		t.Fatal("expected a parse error")
	}
	if errs.KindOf(err) != errs.KindParse {
		t.Errorf("kind = %v (%v)", errs.KindOf(err), err)
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("path = %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"data":[{"id":"b-model"},{"id":"a-model"}]}`)
	}))
	defer srv.Close()
	models, err := NewOpenAI(testOpts(srv.URL)).ListModels(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0].ID != "a-model" {
		t.Errorf("models = %+v", models)
	}
}

func TestAnthropicStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			t.Errorf("path = %s", r.URL.Path)
		}
		if v := r.Header.Get("anthropic-version"); v == "" {
			t.Error("missing anthropic-version header")
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["system"] != "be brief" {
			t.Errorf("system = %v", body["system"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, f := range []string{
			`{"type":"message_start","message":{"model":"claude-x","usage":{"input_tokens":9,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hi "}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"there"}}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"read_file"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"a\"}"}}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`,
		} {
			fmt.Fprintf(w, "event: x\ndata: %s\n\n", f)
		}
	}))
	defer srv.Close()

	p := NewAnthropic(testOpts(srv.URL + "/v1"))
	resp, err := CollectStream(context.Background(), p, Request{
		Model:     "claude-x",
		System:    "be brief",
		Messages:  []Message{{Role: RoleUser, Content: "hi"}},
		Tools:     []ToolSpec{{Name: "read_file", Parameters: JSONSchema(nil)}},
		MaxTokens: 256,
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Message.Content != "Hi there" {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	if resp.Message.ToolCalls[0].Arguments != `{"path":"a"}` {
		t.Errorf("arguments = %q", resp.Message.ToolCalls[0].Arguments)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
	if resp.Usage.InputTokens != 9 || resp.Usage.OutputTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestAnthropicMessageConversion(t *testing.T) {
	msgs := []Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "t1", Name: "read_file", Arguments: `{"path":"x"}`}}},
		{Role: RoleTool, ToolCallID: "t1", Name: "read_file", Content: "contents"},
	}
	out := convertAnthropicMessages(msgs)
	if len(out) != 3 {
		t.Fatalf("messages = %d: %+v", len(out), out)
	}
	if out[0].Role != "user" || out[0].Content[0].Text != "hello" {
		t.Errorf("user turn = %+v", out[0])
	}
	if out[1].Content[0].Type != "tool_use" {
		t.Errorf("assistant turn = %+v", out[1])
	}
	if out[2].Role != "user" || out[2].Content[0].ToolUseID != "t1" {
		t.Errorf("tool result turn = %+v", out[2])
	}
}

func TestGeminiStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("alt") != "sse" {
			t.Errorf("alt = %q", r.URL.Query().Get("alt"))
		}
		if !strings.Contains(r.URL.Path, "streamGenerateContent") {
			t.Errorf("path = %s", r.URL.Path)
		}
		sse(w,
			`{"candidates":[{"content":{"parts":[{"text":"Bonjour"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"read_file","args":{"path":"b.go"}}}]}}]}`,
			`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":4,"totalTokenCount":7}}`,
		)
	}))
	defer srv.Close()

	p := NewGemini(testOpts(srv.URL))
	resp, err := CollectStream(context.Background(), p, Request{
		Model:    "gemini-x",
		System:   "sys",
		Messages: []Message{{Role: RoleUser, Content: "salut"}},
		Tools:    []ToolSpec{{Name: "read_file", Parameters: JSONSchema(nil)}},
	}, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if resp.Message.Content != "Bonjour" {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Name != "read_file" {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	var args map[string]string
	if err := resp.Message.ToolCalls[0].ParsedArguments(&args); err != nil {
		t.Fatal(err)
	}
	if args["path"] != "b.go" {
		t.Errorf("args = %v", args)
	}
	if resp.Usage.TotalTokens != 7 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestGeminiContentConversion(t *testing.T) {
	out := convertGeminiContents([]Message{
		{Role: RoleSystem, Content: "sys"},
		{Role: RoleUser, Content: "hi"},
		{Role: RoleAssistant, Content: "thinking", ToolCalls: []ToolCall{{Name: "t", Arguments: `{"a":1}`}}},
		{Role: RoleTool, Name: "t", Content: "result"},
	})
	if len(out) != 3 {
		t.Fatalf("contents = %+v", out)
	}
	if out[1].Role != "model" || len(out[1].Parts) != 2 {
		t.Errorf("assistant content = %+v", out[1])
	}
	if out[2].Parts[0].FunctionResponse == nil {
		t.Errorf("function response missing: %+v", out[2])
	}
}

func TestRegistryConstruction(t *testing.T) {
	for _, name := range []string{"openai", "anthropic", "gemini", "openrouter", "ollama", "llamacpp", "custom", "mock"} {
		p, err := New(name, Options{BaseURL: "http://127.0.0.1:1/v1", Model: "m", APIKey: "k"})
		if err != nil {
			t.Errorf("New(%q): %v", name, err)
			continue
		}
		if p.Name() == "" {
			t.Errorf("provider %q has no name", name)
		}
	}
	if _, err := New("nope", Options{}); err == nil {
		t.Error("expected an error for an unknown provider")
	}
}

func TestValidate(t *testing.T) {
	if err := Validate("openai", Options{Model: "m"}); err == nil {
		t.Error("missing key should fail validation")
	}
	if err := Validate("openai", Options{APIKey: "k"}); err == nil {
		t.Error("missing model should fail validation")
	}
	if err := Validate("ollama", Options{Model: "m"}); err != nil {
		t.Errorf("local provider without key should be valid: %v", err)
	}
}

func TestDefaultBaseURLsCoverProviders(t *testing.T) {
	for _, name := range Providers() {
		if name == "mock" || name == "custom" {
			continue
		}
		if DefaultBaseURLs[name] == "" {
			t.Errorf("provider %q has no default base URL", name)
		}
	}
}

func TestMockProviderRunsAgentShapedFlow(t *testing.T) {
	m := NewMock(Options{})
	m.Script = []MockTurn{
		{Text: "Let me look.", Tools: []MockToolCall{{Name: "read_file", Arguments: map[string]any{"path": "a.go"}}}},
		{Text: "All good."},
	}
	resp, err := CollectStream(context.Background(), m, Request{
		Model:    "mock-coder",
		Messages: []Message{{Role: RoleUser, Content: "check a.go"}},
		Tools:    []ToolSpec{{Name: "read_file"}},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "Let me look." {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected a tool call, got %+v", resp.Message.ToolCalls)
	}
	resp2, err := CollectStream(context.Background(), m, Request{
		Model: "mock-coder",
		Messages: []Message{
			{Role: RoleUser, Content: "check a.go"},
			resp.Message,
			{Role: RoleTool, Name: "read_file", ToolCallID: "mock_call_1", Content: "package main"},
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp2.Message.Content != "All good." {
		t.Errorf("second turn content = %q", resp2.Message.Content)
	}
}

func TestToolCallArgumentsValidation(t *testing.T) {
	call := ToolCall{Name: "x", Arguments: "{invalid}"}
	var v map[string]any
	err := call.ParsedArguments(&v)
	if err == nil {
		t.Fatal("expected a JSON error")
	}
	if !strings.Contains(err.Error(), "x") {
		t.Errorf("error should mention the tool: %v", err)
	}
	if err := (ToolCall{Arguments: ""}).ParsedArguments(&v); err != nil {
		t.Errorf("empty arguments should be accepted: %v", err)
	}
}

func TestMessageTextSummary(t *testing.T) {
	if got := (Message{Role: RoleAssistant, ToolCalls: []ToolCall{{Name: "a"}, {Name: "b"}}}).Text(); got != "[a, b]" {
		t.Errorf("Text = %q", got)
	}
	if got := (Message{Content: "hi"}).Text(); got != "hi" {
		t.Errorf("Text = %q", got)
	}
}

func TestErrorTypesAreClassified(t *testing.T) {
	if errs.KindOf(errors.New("plain")) != errs.KindInternal {
		t.Error("plain errors should be internal")
	}
	if errs.Retryable(errs.New(errs.KindRateLimit, "x", "y")) != true {
		t.Error("rate limits should be retryable")
	}
	if errs.Retryable(errs.New(errs.KindAuth, "x", "y")) {
		t.Error("auth errors should not be retryable")
	}
}
