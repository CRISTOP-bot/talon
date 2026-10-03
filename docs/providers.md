# Providers

Talon talks to models through one interface, so switching provider is a
configuration change, never a rebuild.

```go
type Provider interface {
    Name() string
    ListModels(ctx context.Context) ([]ModelInfo, error)
    Stream(ctx context.Context, req Request, onEvent func(StreamEvent) error) error
    Complete(ctx context.Context, req Request) (Response, error)
}
```

`Stream` is the primitive: it delivers text deltas, reasoning deltas, complete
tool calls, usage and a final event. `Complete` is a thin helper over it, and the
agent only ever sees the neutral events.

## Built-in providers

| `model.provider` | Protocol | Default base URL |
|---|---|---|
| `openai` | `/chat/completions` | `https://api.openai.com/v1` |
| `anthropic` | `/messages` | `https://api.anthropic.com/v1` |
| `gemini` | `:streamGenerateContent?alt=sse` | `https://generativelanguage.googleapis.com/v1beta` |
| `openrouter` | `/chat/completions` | `https://openrouter.ai/api/v1` |
| `ollama` | `/chat/completions` | `http://localhost:11434/v1` |
| `llamacpp` | `/chat/completions` | `http://localhost:8080/v1` |
| `custom` | `/chat/completions` | from `model.base_url` |

Anything else is treated as an OpenAI-compatible endpoint, which covers most
gateways and proxies.

## Local models

```bash
# Ollama
ollama serve
ollama pull qwen2.5-coder:7b
talon config set model.provider ollama
talon config set model.name qwen2.5-coder:7b
talon

# llama.cpp server
./llama-server -m model.gguf --port 8080
talon config set model.provider llamacpp
talon config set model.name local-model
```

Local providers need no API key. Small models work, but expect more steps: give
them `agent.max_steps = 40` and a lower `model.temperature` (0.1–0.2), and keep
the task narrow.

## Tool calling

All built-in providers support tool calling, which is what makes the agent a
loop rather than a chatbot:

- OpenAI streams `choices[].delta.tool_calls`, with argument fragments
  reassembled per index.
- Anthropic streams `content_block_start` plus `input_json_delta` fragments,
  emitted as one complete `tool_use` block.
- Gemini emits whole `functionCall` parts.

If a provider answers with malformed tool arguments, the arguments are dropped
and the tool reports a validation error to the model, which can then correct
itself.

## Errors

Provider failures are mapped to Talon's error taxonomy and shown as a message
plus a hint:

| HTTP / condition | Kind | Behaviour |
|---|---|---|
| 401, 403 | `auth` | not retried; hint points at the key |
| 404 | `not-found` | hint suggests checking the model name |
| 429 | `rate-limit` | retried, honouring `Retry-After` |
| 400 | `model` | not retried; the message is shown verbatim |
| 5xx, network, timeout | `network` / `timeout` | retried with exponential backoff |
| malformed stream | `parse` | reported, not retried |

## Adding a provider

1. Create `internal/llm/<name>.go` implementing `Stream` (and rely on the
   `Complete` helper).
2. Reuse `client.openStream`, `readSSE` and `statusError` for transport concerns.
3. Register it in `registry.go`, add its base URL to `DefaultBaseURLs` and its
   model catalogue to `DefaultModelsFor`.
4. Test with `httptest` replaying real SSE frames — never a live API.

```go
func MyProvider(ctx context.Context, opts Options) (Provider, error) { … }
```
