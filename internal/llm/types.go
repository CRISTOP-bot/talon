// Package llm defines the provider-agnostic model interface Talon's agent uses.
package llm

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Role is the author of a message.
type Role string

// Message roles.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry of the conversation.
type Message struct {
	Role Role `json:"role"`
	// Content is the plain text content.
	Content string `json:"content,omitempty"`
	// Name identifies the speaker for tool results.
	Name string `json:"name,omitempty"`
	// ToolCallID links a tool result to the call that requested it.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolCalls are the calls the model wants to run (assistant messages).
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	// Reasoning holds provider reasoning summaries when the model exposes
	// them. Talon never streams it to the terminal by default.
	Reasoning string `json:"reasoning,omitempty"`
}

// ToolCall is a model request to run a tool.
type ToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Arguments is the raw JSON arguments object as produced by the model.
	Arguments string `json:"arguments"`
}

// ParsedArguments decodes the call arguments into v.
func (t ToolCall) ParsedArguments(v any) error {
	if strings.TrimSpace(t.Arguments) == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(t.Arguments), v); err != nil {
		return fmt.Errorf("invalid JSON arguments for %s: %w", t.Name, err)
	}
	return nil
}

// Text renders the message as a short human summary (for history views).
func (m Message) Text() string {
	if m.Content != "" {
		return m.Content
	}
	if len(m.ToolCalls) > 0 {
		names := make([]string, 0, len(m.ToolCalls))
		for _, c := range m.ToolCalls {
			names = append(names, c.Name)
		}
		return "[" + strings.Join(names, ", ") + "]"
	}
	return ""
}

// ToolSpec describes a tool to the model in a provider-neutral way.
type ToolSpec struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object describing the arguments.
	Parameters map[string]any
}

// JSONSchema is a helper for building the common object schemas tools use.
func JSONSchema(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	schema := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		schema["required"] = required
	} else {
		schema["required"] = []string{}
	}
	return schema
}

// Prop is a shorthand for a schema property.
func Prop(kind, description string) map[string]any {
	return map[string]any{"type": kind, "description": description}
}

// Enum is a shorthand for a string property with a fixed set of values.
func Enum(description string, values ...string) map[string]any {
	list := make([]any, len(values))
	for i, v := range values {
		list[i] = v
	}
	return map[string]any{"type": "string", "description": description, "enum": list}
}

// Usage reports token consumption.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
	// CacheReadTokens is reported by Anthropic; kept for display purposes.
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
}

// StopReason explains why the model stopped.
type StopReason string

// Stop reasons.
const (
	StopEnd       StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopCanceled  StopReason = "canceled"
	StopError     StopReason = "error"
)

// Request is a single completion request.
type Request struct {
	Model       string
	System      string
	Messages    []Message
	Tools       []ToolSpec
	Temperature float64
	TopP        float64
	MaxTokens   int
	Stop        []string
	// Metadata is passed through to providers that support it.
	Metadata map[string]string
}

// Response is a completed (non-streaming) reply.
type Response struct {
	Message    Message
	Usage      Usage
	StopReason StopReason
	// Model is the model that actually served the request.
	Model string
	// Raw holds the provider payload for debugging.
	Raw string
}

// StreamEvent is one event of a streamed reply.
type StreamEvent struct {
	// Type is one of: text, reasoning, tool_call, usage, done, error.
	Type string
	// Text carries text or reasoning deltas.
	Text string
	// ToolCall is set for tool_call events.
	ToolCall *ToolCall
	// Usage is set on the usage event.
	Usage *Usage
	// Response is set on the done event and carries the assembled message.
	Response *Response
	// StopReason accompanies the done event.
	StopReason StopReason
	// Err accompanies the error event.
	Err error
}

// Stream event types.
const (
	EventText      = "text"
	EventReasoning = "reasoning"
	EventToolCall  = "tool_call"
	EventUsage     = "usage"
	EventDone      = "done"
	EventError     = "error"
)

// ModelInfo describes a model offered by a provider.
type ModelInfo struct {
	ID            string `json:"id"`
	DisplayName   string `json:"display_name,omitempty"`
	ContextWindow int    `json:"context_window,omitempty"`
	// Input/Output modalities let the UI hide unsupported models.
	InputModalities  []string `json:"input_modalities,omitempty"`
	OutputModalities []string `json:"output_modalities,omitempty"`
}

// Local reports whether the model runs on the user's machine.
func (m ModelInfo) Local() bool {
	return strings.HasPrefix(m.ID, "local") || strings.Contains(m.ID, "localhost")
}

// ModelsURL is the endpoint used to list models.
const ModelsURL = "https://models.dev/api.json"

// DefaultModelsFor returns a sensible starting model per provider, used when
// the user has not configured one.
func DefaultModelsFor(provider string) []ModelInfo {
	switch provider {
	case "openai":
		return []ModelInfo{
			{ID: "gpt-5", DisplayName: "GPT-5", ContextWindow: 400000},
			{ID: "gpt-5-mini", DisplayName: "GPT-5 mini", ContextWindow: 400000},
			{ID: "gpt-4.1", DisplayName: "GPT-4.1", ContextWindow: 1047576},
		}
	case "anthropic":
		return []ModelInfo{
			{ID: "claude-sonnet-4-5", DisplayName: "Claude Sonnet 4.5", ContextWindow: 200000},
			{ID: "claude-opus-4-1", DisplayName: "Claude Opus 4.1", ContextWindow: 200000},
			{ID: "claude-haiku-4-5", DisplayName: "Claude Haiku 4.5", ContextWindow: 200000},
		}
	case "gemini":
		return []ModelInfo{
			{ID: "gemini-2.5-pro", DisplayName: "Gemini 2.5 Pro", ContextWindow: 1048576},
			{ID: "gemini-2.5-flash", DisplayName: "Gemini 2.5 Flash", ContextWindow: 1048576},
		}
	case "openrouter":
		return []ModelInfo{
			{ID: "anthropic/claude-sonnet-4.5", DisplayName: "Claude Sonnet 4.5", ContextWindow: 200000},
			{ID: "openai/gpt-5", DisplayName: "GPT-5", ContextWindow: 400000},
		}
	case "ollama":
		return []ModelInfo{
			{ID: "qwen2.5-coder:7b", DisplayName: "Qwen2.5 Coder 7B (local)", ContextWindow: 32768},
			{ID: "llama3.1:8b", DisplayName: "Llama 3.1 8B (local)", ContextWindow: 131072},
		}
	case "llamacpp":
		return []ModelInfo{{ID: "local-model", DisplayName: "llama.cpp local server", ContextWindow: 32768}}
	case "mock":
		return []ModelInfo{{ID: "mock-coder", DisplayName: "Deterministic offline test model"}}
	default:
		return nil
	}
}

// SortModels orders models by display name for stable listings.
func SortModels(models []ModelInfo) {
	sort.Slice(models, func(i, j int) bool {
		if models[i].DisplayName == models[j].DisplayName {
			return models[i].ID < models[j].ID
		}
		return models[i].DisplayName < models[j].DisplayName
	})
}
