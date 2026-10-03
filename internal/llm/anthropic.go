package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/errs"
)

// Anthropic implements the /messages API.
type Anthropic struct {
	opts    Options
	client  *client
	baseURL string
}

// NewAnthropic builds an Anthropic provider.
func NewAnthropic(opts Options) *Anthropic {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://api.anthropic.com/v1"
	}
	return &Anthropic{opts: opts, client: newClient(opts), baseURL: base}
}

// Name implements Provider.
func (p *Anthropic) Name() string { return "anthropic" }

// anthropicVersion is the API version header Talon pins.
const anthropicVersion = "2023-06-01"

type anthropicRequest struct {
	Model         string             `json:"model"`
	System        string             `json:"system,omitempty"`
	Messages      []anthropicMessage `json:"messages"`
	Tools         []anthropicTool    `json:"tools,omitempty"`
	MaxTokens     int                `json:"max_tokens"`
	Temperature   float64            `json:"temperature,omitempty"`
	TopP          float64            `json:"top_p,omitempty"`
	Stream        bool               `json:"stream"`
	StopSequences []string           `json:"stop_sequences,omitempty"`
}

type anthropicMessage struct {
	Role    string             `json:"role"`
	Content []anthropicContent `json:"content"`
}

type anthropicContent struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
	IsError   bool           `json:"is_error,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

// ListModels implements Provider.
func (p *Anthropic) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var payload struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		} `json:"data"`
	}
	err := p.client.do(ctx, http.MethodGet, p.baseURL+"/models", nil, &payload)
	if err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(payload.Data))
	for _, m := range payload.Data {
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		out = append(out, ModelInfo{ID: m.ID, DisplayName: name})
	}
	SortModels(out)
	return out, nil
}

// Stream implements Provider.
func (p *Anthropic) Stream(ctx context.Context, req Request, onEvent func(StreamEvent) error) error {
	if req.MaxTokens <= 0 {
		req.MaxTokens = p.opts.MaxOutputTokens
	}
	if req.MaxTokens <= 0 {
		req.MaxTokens = 4096
	}
	body := anthropicRequest{
		Model:         req.Model,
		System:        req.System,
		Messages:      convertAnthropicMessages(req.Messages),
		MaxTokens:     req.MaxTokens,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		Stream:        true,
		StopSequences: req.Stop,
	}
	for _, t := range req.Tools {
		schema := t.Parameters
		if schema == nil {
			schema = JSONSchema(nil)
		}
		body.Tools = append(body.Tools, anthropicTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		})
	}

	resp, err := p.anthropicStream(ctx, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var content strings.Builder
	var reasoning strings.Builder
	usage := Usage{}
	stopReason := StopEnd
	model := p.opts.Model

	// Tool-use blocks are streamed as content_block_start + input deltas +
	// content_block_stop; they are buffered per index.
	toolBlocks := map[int]*struct {
		id    string
		name  string
		input strings.Builder
	}{}
	toolOrder := []int{}

	err = readSSE(ctx, resp.Body, func(ev sseEvent) error {
		if strings.TrimSpace(ev.data) == "[DONE]" {
			return nil
		}
		var frame map[string]any
		if jerr := json.Unmarshal([]byte(ev.data), &frame); jerr != nil {
			return errs.Parse("anthropic", "malformed stream frame: %v", jerr)
		}
		typ, _ := frame["type"].(string)
		switch typ {
		case "message_start":
			if msg, ok := frame["message"].(map[string]any); ok {
				if m, ok := msg["model"].(string); ok && m != "" {
					model = m
				}
				if u, ok := msg["usage"].(map[string]any); ok {
					usage.InputTokens = intOf(u["input_tokens"])
					usage.OutputTokens = intOf(u["output_tokens"])
				}
			}
		case "content_block_start":
			idx := intOf(frame["index"])
			if blk, ok := frame["content_block"].(map[string]any); ok {
				if btype, _ := blk["type"].(string); btype == "tool_use" {
					toolBlocks[idx] = &struct {
						id    string
						name  string
						input strings.Builder
					}{id: str(blk["id"]), name: str(blk["name"])}
					toolOrder = append(toolOrder, idx)
				}
			}
		case "content_block_delta":
			idx := intOf(frame["index"])
			delta, _ := frame["delta"].(map[string]any)
			dtype, _ := delta["type"].(string)
			switch dtype {
			case "text_delta":
				txt := str(delta["text"])
				if txt != "" {
					content.WriteString(txt)
					if err := onEvent(StreamEvent{Type: EventText, Text: txt}); err != nil {
						return err
					}
				}
			case "thinking_delta":
				txt := str(delta["thinking"])
				if txt != "" {
					reasoning.WriteString(txt)
					if err := onEvent(StreamEvent{Type: EventReasoning, Text: txt}); err != nil {
						return err
					}
				}
			case "input_json_delta":
				if tb, ok := toolBlocks[idx]; ok {
					tb.input.WriteString(str(delta["partial_json"]))
				}
			}
		case "message_delta":
			if delta, ok := frame["delta"].(map[string]any); ok {
				stopReason = anthropicStopReason(str(delta["stop_reason"]))
			}
			if u, ok := frame["usage"].(map[string]any); ok {
				if n := intOf(u["output_tokens"]); n > 0 {
					usage.OutputTokens = n
				}
			}
		case "error":
			msg := "the provider reported an error"
			if e, ok := frame["error"].(map[string]any); ok {
				if m := str(e["message"]); m != "" {
					msg = m
				}
			}
			return errs.Newf(errs.KindModel, "anthropic", "%s", msg)
		}
		return nil
	})
	if err != nil {
		return err
	}

	for _, idx := range toolOrder {
		tb := toolBlocks[idx]
		args := tb.input.String()
		if strings.TrimSpace(args) == "" {
			args = "{}"
		}
		call := ToolCall{ID: tb.id, Name: tb.name, Arguments: args}
		if err := onEvent(StreamEvent{Type: EventToolCall, ToolCall: &call}); err != nil {
			return err
		}
	}
	usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	if u := &usage; u.TotalTokens > 0 {
		if err := onEvent(StreamEvent{Type: EventUsage, Usage: u}); err != nil {
			return err
		}
	}
	final := Response{
		Message: Message{
			Role:      RoleAssistant,
			Content:   content.String(),
			Reasoning: reasoning.String(),
		},
		Usage: usage,
		Model: model,
	}
	return onEvent(StreamEvent{Type: EventDone, Response: &final, StopReason: stopReason})
}

// anthropicStream issues the streaming request with Anthropic's headers.
func (p *Anthropic) anthropicStream(ctx context.Context, body anthropicRequest) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, errs.Internal("anthropic", "cannot encode request: %v", err)
	}
	var out *http.Response
	err = p.client.withRetry(ctx, func() error {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/messages", strings.NewReader(string(payload)))
		if rerr != nil {
			return errs.Internal("anthropic", "cannot build request: %v", rerr)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("anthropic-version", anthropicVersion)
		req.Header.Set("User-Agent", p.client.userInfo)
		if p.opts.APIKey != "" {
			req.Header.Set("x-api-key", p.opts.APIKey)
		}
		for k, v := range p.opts.Headers {
			req.Header.Set(k, v)
		}
		resp, derr := p.client.http.Do(req)
		if derr != nil {
			return classifyTransportError(ctx, derr)
		}
		if resp.StatusCode >= 400 {
			defer resp.Body.Close()
			return statusError(resp)
		}
		out = resp
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// convertAnthropicMessages maps neutral messages to Anthropic's content blocks,
// merging tool results into a single user turn as the API requires.
func convertAnthropicMessages(msgs []Message) []anthropicMessage {
	var out []anthropicMessage
	appendTo := func(role string, block anthropicContent) {
		if n := len(out); n > 0 && out[n-1].Role == role {
			out[n-1].Content = append(out[n-1].Content, block)
			return
		}
		out = append(out, anthropicMessage{Role: role, Content: []anthropicContent{block}})
	}
	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			continue
		case RoleUser, RoleAssistant:
			role := "user"
			if m.Role == RoleAssistant {
				role = "assistant"
			}
			if m.Content != "" {
				appendTo(role, anthropicContent{Type: "text", Text: m.Content})
			}
			for _, c := range m.ToolCalls {
				var input map[string]any
				if strings.TrimSpace(c.Arguments) != "" {
					_ = json.Unmarshal([]byte(c.Arguments), &input)
				}
				if input == nil {
					input = map[string]any{}
				}
				appendTo("assistant", anthropicContent{
					Type: "tool_use", ID: c.ID, Name: c.Name, Input: input,
				})
			}
		case RoleTool:
			block := anthropicContent{
				Type:      "tool_result",
				ToolUseID: m.ToolCallID,
				Content:   m.Content,
			}
			if strings.HasPrefix(m.Content, "error:") {
				block.IsError = true
			}
			appendTo("user", block)
		}
	}
	return out
}

func anthropicStopReason(s string) StopReason {
	switch s {
	case "tool_use":
		return StopToolUse
	case "max_tokens":
		return StopMaxTokens
	default:
		return StopEnd
	}
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	}
	return 0
}

// Complete implements Provider.
func (p *Anthropic) Complete(ctx context.Context, req Request) (Response, error) {
	return Complete(ctx, p, req)
}
