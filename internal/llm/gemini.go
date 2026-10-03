package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/CRISTOP-bot/talon/internal/errs"
)

// Gemini implements Google's generateContent streaming API.
type Gemini struct {
	opts    Options
	client  *client
	baseURL string
	apiKey  string
}

// NewGemini builds a Gemini provider. Gemini authenticates with a query
// parameter rather than a header.
func NewGemini(opts Options) *Gemini {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "https://generativelanguage.googleapis.com/v1beta"
	}
	return &Gemini{opts: opts, client: newClient(opts), baseURL: base, apiKey: opts.APIKey}
}

// Name implements Provider.
func (p *Gemini) Name() string { return "gemini" }

type geminiRequest struct {
	Contents          []geminiContent  `json:"contents"`
	SystemInstruction *geminiContent   `json:"systemInstruction,omitempty"`
	Tools             []geminiTool     `json:"tools,omitempty"`
	GenerationConfig  *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text             string          `json:"text,omitempty"`
	FunctionCall     *geminiFuncCall `json:"functionCall,omitempty"`
	FunctionResponse *geminiFuncResp `json:"functionResponse,omitempty"`
}

type geminiFuncCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type geminiFuncResp struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type geminiTool struct {
	FunctionDeclarations []geminiFuncDecl `json:"functionDeclarations"`
}

type geminiFuncDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type geminiGenConfig struct {
	Temperature *float64 `json:"temperature,omitempty"`
	TopP        *float64 `json:"topP,omitempty"`
	MaxTokens   int      `json:"maxOutputTokens,omitempty"`
	StopSeqs    []string `json:"stopSequences,omitempty"`
}

// ListModels implements Provider.
func (p *Gemini) ListModels(ctx context.Context) ([]ModelInfo, error) {
	var payload struct {
		Models []struct {
			Name                       string   `json:"name"`
			DisplayName                string   `json:"displayName"`
			InputTokenLimit            int      `json:"inputTokenLimit"`
			SupportedGenerationMethods []string `json:"supportedGenerationMethods"`
		} `json:"models"`
	}
	url := p.baseURL + "/models"
	if p.apiKey != "" {
		url += "?key=" + p.apiKey
	}
	if err := p.client.do(ctx, http.MethodGet, url, nil, &payload); err != nil {
		return nil, err
	}
	var out []ModelInfo
	for _, m := range payload.Models {
		if len(m.SupportedGenerationMethods) > 0 && !contains(m.SupportedGenerationMethods, "generateContent") {
			continue
		}
		id := strings.TrimPrefix(m.Name, "models/")
		name := m.DisplayName
		if name == "" {
			name = id
		}
		out = append(out, ModelInfo{ID: id, DisplayName: name, ContextWindow: m.InputTokenLimit})
	}
	SortModels(out)
	return out, nil
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// Stream implements Provider.
func (p *Gemini) Stream(ctx context.Context, req Request, onEvent func(StreamEvent) error) error {
	body := geminiRequest{}
	if req.System != "" {
		body.SystemInstruction = &geminiContent{Parts: []geminiPart{{Text: req.System}}}
	}
	body.Contents = convertGeminiContents(req.Messages)

	if len(req.Tools) > 0 {
		decls := make([]geminiFuncDecl, 0, len(req.Tools))
		for _, t := range req.Tools {
			d := geminiFuncDecl{Name: t.Name, Description: t.Description}
			if t.Parameters != nil {
				d.Parameters = t.Parameters
			}
			decls = append(decls, d)
		}
		body.Tools = []geminiTool{{FunctionDeclarations: decls}}
	}
	cfg := &geminiGenConfig{StopSeqs: req.Stop}
	if req.Temperature > 0 {
		t := req.Temperature
		cfg.Temperature = &t
	}
	if req.TopP > 0 && req.TopP < 1 {
		pv := req.TopP
		cfg.TopP = &pv
	}
	if req.MaxTokens > 0 {
		cfg.MaxTokens = req.MaxTokens
	}
	body.GenerationConfig = cfg

	url := p.baseURL + "/models/" + req.Model + ":streamGenerateContent?alt=sse"
	if p.apiKey != "" {
		url += "&key=" + p.apiKey
	}
	resp, err := p.client.openStream(ctx, url, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var content strings.Builder
	usage := Usage{}
	stopReason := StopEnd
	callSeq := 0

	err = readSSE(ctx, resp.Body, func(ev sseEvent) error {
		data := strings.TrimSpace(ev.data)
		// Some gateways terminate the SSE stream with [DONE].
		if data == "" || data == "[DONE]" {
			return nil
		}
		var frame struct {
			Candidates []struct {
				Content struct {
					Parts []geminiPart `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			UsageMetadata *struct {
				PromptTokenCount     int `json:"promptTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
				TotalTokenCount      int `json:"totalTokenCount"`
			} `json:"usageMetadata"`
		}
		if jerr := json.Unmarshal([]byte(data), &frame); jerr != nil {
			return errs.Parse("gemini", "malformed stream frame: %v", jerr)
		}
		if frame.UsageMetadata != nil {
			usage = Usage{
				InputTokens:  frame.UsageMetadata.PromptTokenCount,
				OutputTokens: frame.UsageMetadata.CandidatesTokenCount,
				TotalTokens:  frame.UsageMetadata.TotalTokenCount,
			}
		}
		for _, cand := range frame.Candidates {
			for _, part := range cand.Content.Parts {
				switch {
				case part.Text != "":
					content.WriteString(part.Text)
					if err := onEvent(StreamEvent{Type: EventText, Text: part.Text}); err != nil {
						return err
					}
				case part.FunctionCall != nil:
					callSeq++
					args := "{}"
					if len(part.FunctionCall.Args) > 0 {
						if encoded, merr := json.Marshal(part.FunctionCall.Args); merr == nil {
							args = string(encoded)
						}
					}
					call := ToolCall{
						ID:        "gemini_call_" + itoa(callSeq),
						Name:      part.FunctionCall.Name,
						Arguments: args,
					}
					if err := onEvent(StreamEvent{Type: EventToolCall, ToolCall: &call}); err != nil {
						return err
					}
				}
			}
			if cand.FinishReason != "" {
				stopReason = mapFinishReason(cand.FinishReason, callSeq > 0)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if u := &usage; u.TotalTokens > 0 {
		if err := onEvent(StreamEvent{Type: EventUsage, Usage: u}); err != nil {
			return err
		}
	}
	final := Response{
		Message: Message{Role: RoleAssistant, Content: content.String()},
		Usage:   usage,
		Model:   req.Model,
	}
	return onEvent(StreamEvent{Type: EventDone, Response: &final, StopReason: stopReason})
}

func convertGeminiContents(msgs []Message) []geminiContent {
	var out []geminiContent
	for _, m := range msgs {
		switch m.Role {
		case RoleSystem:
			continue
		case RoleAssistant:
			parts := []geminiPart{}
			if m.Content != "" {
				parts = append(parts, geminiPart{Text: m.Content})
			}
			for _, c := range m.ToolCalls {
				var args map[string]any
				if strings.TrimSpace(c.Arguments) != "" {
					_ = json.Unmarshal([]byte(c.Arguments), &args)
				}
				parts = append(parts, geminiPart{FunctionCall: &geminiFuncCall{Name: c.Name, Args: args}})
			}
			if len(parts) > 0 {
				out = append(out, geminiContent{Role: "model", Parts: parts})
			}
		case RoleTool:
			payload := map[string]any{"result": m.Content}
			if strings.HasPrefix(m.Content, "error:") {
				payload["error"] = strings.TrimPrefix(m.Content, "error:")
			}
			out = append(out, geminiContent{
				Role:  "user",
				Parts: []geminiPart{{FunctionResponse: &geminiFuncResp{Name: m.Name, Response: payload}}},
			})
		default:
			if m.Content == "" {
				continue
			}
			out = append(out, geminiContent{Role: "user", Parts: []geminiPart{{Text: m.Content}}})
		}
	}
	return out
}

// Complete implements Provider.
func (p *Gemini) Complete(ctx context.Context, req Request) (Response, error) {
	return Complete(ctx, p, req)
}

// itoa converts small positive integers to strings (used for call ids).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
