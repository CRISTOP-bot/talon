package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/talon-cli/talon/internal/errs"
)

// OpenAI implements the /chat/completions protocol, which is also spoken by
// OpenRouter, Ollama, llama.cpp's server, LM Studio, vLLM and most gateways.
type OpenAI struct {
	opts   Options
	client *client
	// baseURL is the API root, e.g. https://api.openai.com/v1
	baseURL string
}

// NewOpenAI builds an OpenAI-compatible provider.
func NewOpenAI(opts Options) *OpenAI {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://api.openai.com/v1"
	}
	return &OpenAI{opts: opts, client: newClient(opts), baseURL: base}
}

// Name implements Provider.
func (p *OpenAI) Name() string { return "openai" }

// endpoint joins the base URL with a path.
func (p *OpenAI) endpoint(path string) string {
	if strings.HasPrefix(path, "http") {
		return path
	}
	return p.baseURL + "/" + strings.TrimLeft(path, "/")
}

// ListModels implements Provider.
func (p *OpenAI) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var payload struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := p.client.do(ctx, http.MethodGet, p.endpoint("models"), nil, &payload); err != nil {
		return nil, err
	}
	out := make([]ModelInfo, 0, len(payload.Data))
	for _, m := range payload.Data {
		out = append(out, ModelInfo{ID: m.ID, DisplayName: m.ID})
	}
	SortModels(out)
	return out, nil
}

// chatRequest is the wire format for /chat/completions.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Tools       []chatTool    `json:"tools,omitempty"`
	Temperature float64       `json:"temperature,omitempty"`
	TopP        float64       `json:"top_p,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Stream      bool          `json:"stream"`
	Stop        []string      `json:"stop,omitempty"`
}

type chatMessage struct {
	Role       string      `json:"role"`
	Content    any         `json:"content,omitempty"`
	Name       string      `json:"name,omitempty"`
	ToolCalls  []chatToolC `json:"tool_calls,omitempty"`
	ToolCallID string      `json:"tool_call_id,omitempty"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
	} `json:"function"`
}

type chatToolC struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// buildChatRequest converts a neutral Request into the wire format.
func buildChatRequest(req Request, tools []ToolSpec, stream bool) chatRequest {
	out := chatRequest{
		Model:       req.Model,
		Stream:      stream,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Stop:        req.Stop,
	}
	if req.System != "" {
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		out.Messages = append(out.Messages, convertMessage(m))
	}
	for _, t := range tools {
		var ct chatTool
		ct.Type = "function"
		ct.Function.Name = t.Name
		ct.Function.Description = t.Description
		params := t.Parameters
		if params == nil {
			params = JSONSchema(nil)
		}
		ct.Function.Parameters = params
		out.Tools = append(out.Tools, ct)
	}
	return out
}

func convertMessage(m Message) chatMessage {
	cm := chatMessage{Role: string(m.Role), Name: m.Name, ToolCallID: m.ToolCallID}
	if m.Content != "" {
		cm.Content = m.Content
	}
	for _, c := range m.ToolCalls {
		var ctc chatToolC
		ctc.ID = c.ID
		ctc.Type = "function"
		ctc.Function.Name = c.Name
		ctc.Function.Arguments = c.Arguments
		cm.ToolCalls = append(cm.ToolCalls, ctc)
	}
	return cm
}

// chatStreamChunk is one SSE data frame.
type chatStreamChunk struct {
	Choices []struct {
		Delta struct {
			Content   string `json:"content"`
			Reasoning string `json:"reasoning_content"`
			ToolCalls []struct {
				Index    int    `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Model string `json:"model"`
}

// Stream implements Provider.
func (p *OpenAI) Stream(ctx context.Context, req Request, onEvent func(StreamEvent) error) error {
	body := buildChatRequest(req, req.Tools, true)
	resp, err := p.client.openStream(ctx, p.endpoint("chat/completions"), body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// Accumulators for streamed tool calls, keyed by their delta index.
	type partial struct {
		id      string
		name    string
		args    strings.Builder
		started bool
	}
	partials := map[int]*partial{}
	var order []int
	var content strings.Builder
	stopReason := StopEnd
	usage := Usage{}
	model := p.opts.Model

	err = readSSE(ctx, resp.Body, func(ev sseEvent) error {
		data := strings.TrimSpace(ev.data)
		if data == "" || data == "[DONE]" {
			return nil
		}
		var chunk chatStreamChunk
		if jerr := json.Unmarshal([]byte(data), &chunk); jerr != nil {
			return errs.Parse("openai", "malformed stream frame: %v", jerr)
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Usage != nil {
			usage = Usage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
				TotalTokens:  chunk.Usage.TotalTokens,
			}
			if err := onEvent(StreamEvent{Type: EventUsage, Usage: &usage}); err != nil {
				return err
			}
		}
		for _, choice := range chunk.Choices {
			if choice.Delta.Reasoning != "" {
				if err := onEvent(StreamEvent{Type: EventReasoning, Text: choice.Delta.Reasoning}); err != nil {
					return err
				}
			}
			if choice.Delta.Content != "" {
				content.WriteString(choice.Delta.Content)
				if err := onEvent(StreamEvent{Type: EventText, Text: choice.Delta.Content}); err != nil {
					return err
				}
			}
			for _, tc := range choice.Delta.ToolCalls {
				pt, ok := partials[tc.Index]
				if !ok {
					pt = &partial{}
					partials[tc.Index] = pt
					order = append(order, tc.Index)
				}
				if tc.ID != "" {
					pt.id = tc.ID
				}
				if tc.Function.Name != "" {
					pt.name = tc.Function.Name
				}
				pt.args.WriteString(tc.Function.Arguments)
			}
			if choice.FinishReason != "" {
				stopReason = mapFinishReason(choice.FinishReason, len(order) > 0)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, idx := range order {
		pt := partials[idx]
		call := ToolCall{
			ID:   pt.id,
			Name: pt.name,
		}
		if call.ID == "" {
			call.ID = fmt.Sprintf("call_%d_%d", idx, content.Len())
		}
		call.Arguments = pt.args.String()
		if !json.Valid([]byte(call.Arguments)) && strings.TrimSpace(call.Arguments) != "" {
			// Some gateways stream truncated JSON; wrap it so the tool's
			// validation reports a useful error instead of a crash.
			call.Arguments = "{}"
		}
		if err := onEvent(StreamEvent{Type: EventToolCall, ToolCall: &call}); err != nil {
			return err
		}
	}
	final := Response{
		Message: Message{Role: RoleAssistant, Content: content.String()},
		Usage:   usage,
		Model:   model,
	}
	return onEvent(StreamEvent{Type: EventDone, Response: &final, StopReason: stopReason})
}

func mapFinishReason(reason string, hadToolCalls bool) StopReason {
	switch reason {
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	case "stop":
		if hadToolCalls {
			return StopToolUse
		}
		return StopEnd
	default:
		return StopEnd
	}
}

// Complete implements Provider.
func (p *OpenAI) Complete(ctx context.Context, req Request) (Response, error) {
	return Complete(ctx, p, req)
}
