// Command talon-mock-provider is a deterministic, OpenAI-compatible provider.
//
// It exists so Talon can be exercised end to end without an account, a key or a
// network: CI, manual testing of a configuration, and reproducing a bug report
// on someone else's machine.
//
//	talon-mock-provider --port 8080
//	AI_PROVIDER=custom talon config set model.base_url http://127.0.0.1:8080/v1
//	talon
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		addr     string
		model    string
		reply    string
		toolCall string
		latency  time.Duration
		verbose  bool
	)
	fs := flag.NewFlagSet("talon-mock-provider", flag.ContinueOnError)
	fs.StringVar(&addr, "addr", "127.0.0.1:8080", "address to listen on")
	fs.StringVar(&model, "model", "mock-model", "model id to advertise")
	fs.StringVar(&reply, "reply", "", "fixed reply; empty echoes the prompt")
	fs.StringVar(&toolCall, "tool-call", "", "if set, answer with a call to this tool using {\"path\":\".\"}")
	fs.DurationVar(&latency, "latency", 0, "artificial delay before replying")
	fs.BoolVar(&verbose, "v", false, "log requests to stderr")
	fs.Parse(os.Args[1:])

	s := &server{model: model, reply: reply, tool: toolCall, latency: latency, verbose: verbose}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/models", s.models)
	mux.HandleFunc("/v1/chat/completions", s.chat)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	fmt.Fprintf(os.Stderr, "mock provider listening on http://%s/v1 (model %s)\n", addr, model)
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type server struct {
	model   string
	reply   string
	tool    string
	latency time.Duration
	verbose bool
}

func (s *server) models(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data": []map[string]any{{
			"id":       s.model,
			"object":   "model",
			"owned_by": "talon-mock",
		}},
	})
}

// chat implements the subset of /chat/completions that Talon uses, including
// server-sent-event streaming.
func (s *server) chat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Model    string    `json:"model"`
		Stream   bool      `json:"stream"`
		Messages []message `json:"messages"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": map[string]string{"message": "invalid JSON body: " + err.Error()},
		})
		return
	}
	if s.verbose {
		fmt.Fprintf(os.Stderr, "request: model=%s stream=%v messages=%d\n",
			req.Model, req.Stream, len(req.Messages))
	}
	if s.latency > 0 {
		time.Sleep(s.latency)
	}

	prompt := lastUser(req.Messages)
	if s.tool != "" {
		s.respondTool(w, req.Stream)
		return
	}
	text := s.reply
	if text == "" {
		text = "Entendido. " + prompt
	}
	if req.Stream {
		s.streamText(w, text)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      "chatcmpl-mock",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   orDefault(req.Model, s.model),
		"choices": []map[string]any{{
			"index":         0,
			"message":       map[string]string{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]int{"prompt_tokens": len(prompt), "completion_tokens": len(text)},
	})
}

// respondTool answers with a tool call, so the agent loop can be exercised.
func (s *server) respondTool(w http.ResponseWriter, stream bool) {
	id := "call_mock_1"
	toolCall := map[string]any{
		"id":   id,
		"type": "function",
		"function": map[string]string{
			"name":      s.tool,
			"arguments": fmt.Sprintf(`{"path":%q}`, "."),
		},
	}
	if !stream {
		writeJSON(w, http.StatusOK, map[string]any{
			"id":      "chatcmpl-mock",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   s.model,
			"choices": []map[string]any{{
				"index":         0,
				"message":       map[string]any{"role": "assistant", "content": "", "tool_calls": []any{toolCall}},
				"finish_reason": "tool_calls",
			}},
		})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	first := map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
		"model":   s.model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "tool_calls": []any{toolCall}}, "finish_reason": nil}},
	}
	writeSSE(w, first)
	if flusher != nil {
		flusher.Flush()
	}
	writeSSE(w, map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
		"model":   s.model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}},
	})
	if flusher != nil {
		flusher.Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

// streamText replies in small chunks so the client's streaming path is used.
func (s *server) streamText(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	writeSSE(w, map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
		"model":   s.model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": ""}, "finish_reason": nil}},
	})
	for _, word := range strings.SplitAfter(text, " ") {
		if word == "" {
			continue
		}
		writeSSE(w, map[string]any{
			"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
			"model":   s.model,
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{"content": word}, "finish_reason": nil}},
		})
		if flusher != nil {
			flusher.Flush()
		}
	}
	writeSSE(w, map[string]any{
		"id": "chatcmpl-mock", "object": "chat.completion.chunk", "created": time.Now().Unix(),
		"model":   s.model,
		"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
	})
	if flusher != nil {
		flusher.Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeSSE(w http.ResponseWriter, payload map[string]any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// message is one entry of a chat request.
type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// lastUser returns the content of the most recent user message.
func lastUser(messages []message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
}

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
