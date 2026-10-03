# Architecture

This document explains how Talon is put together and, more importantly, why.

## Design goals

1. **The model is never trusted with the machine.** It can only call registered
   tools; every call passes validation, the permission policy and, when needed,
   the user.
2. **Bounded autonomy.** A step budget, a context budget and a permission level
   keep every run finite and predictable.
3. **Swappable boundaries.** Providers, shells, storage and rendering sit behind
   interfaces, so adding an alternative does not mean rewriting the product.
4. **One binary, no runtime.** The whole thing is Go standard library, which
   makes it fast to start, trivial to distribute and reproducible to build.

## Layers

```
                 ┌──────────────────────────────────────────────┐
   terminal  →   │ cli ── commands ── repl (loop, slash cmds)   │
                 └───────────────────────┬──────────────────────┘
                                         │
                 ┌───────────────────────▼──────────────────────┐
                 │ agent  (the loop and its state machine)      │
                 └───┬───────────────────┬──────────────────┬───┘
                     │                   │                  │
            ┌────────▼───────┐  ┌────────▼──────┐  ┌────────▼────────┐
            │ tools registry │  │ llm providers │  │ context builder │
            └────────┬───────┘  └───────────────┘  └─────────────────┘
                     │
   ┌─────────┬───────┼────────┬──────────┬──────────┐
 index     shell    git    journal    project    perm
   │         │        │        │           │         │
   └─────────┴────────┴────────┴───────────┴─────────┴──► fs / processes
```

Each arrow points downward only. Nothing below knows about the agent, which is
what keeps the loop testable and the tools replaceable.

## The agent loop

`internal/agent` is deliberately explicit. One turn is:

```go
for step := 1; step <= maxSteps; step++ {
    assistant, toolCalls, usage := callModel()   // stream text to the UI
    remember(assistant)
    if no toolCalls { return }
    for _, call := range toolCalls {
        runTool(call)                            // validate → permit → execute → remember
    }
}
```

Everything the loop does is emitted as an `Event` (`turn_start`, `text`,
`tool_start`, `tool_approval`, `tool_result`, `plan`, `turn_end`, `error`,
`notice`). The REPL renders events; the session log stores them. Nothing in the
loop formats text for humans, which is why the same loop serves the interactive
UI, the one-shot mode and the tests.

Failure handling is part of the loop, not an afterthought:

- A tool error becomes a tool result the model can react to, not a dead end.
- A rejected tool call tells the model it was rejected and why.
- A provider error aborts the turn with a mapped, human-readable error.
- Hitting `max_steps` appends an instruction telling the model to wrap up, so
  the transcript stays coherent.

## Context strategy

Talon never sends the repository. The system prompt contains:

1. A short behavioural contract (how to work, what not to do).
2. The project description produced by `internal/project` (language, build
   system, package manager, test layout, entry points, git state).
3. An index summary (file counts, language breakdown, hot directories).
4. Remembered notes and any committed memory file.
5. A one-line tool guide — the JSON schemas carry the argument detail.

Everything else is fetched on demand by the model through tools. That keeps the
fixed cost low and makes the context relevant to the request.

`internal/agent.Conversation` tracks an estimated token count and compacts by
asking the model for a digest when it approaches `context.compact_at`. The
digest replaces whole turns — never a request without its answer.

## Permissions

`internal/perm` is a pure decision function: `(level, request) → allow | ask |
deny`, plus a reason. It is deliberately free of I/O so it can be reasoned about
and tested exhaustively (see `perm_test.go`).

Two rules are absolute regardless of level: deny lists, and path containment.
`full-access` relaxes confirmations, not those.

The agent asks the policy, then asks the user (`repl.Approver`), then records the
answer as a one-shot or permanent approval.

## Providers

`llm.Provider` is intentionally small: `Stream` is the primitive, `Complete` is
a helper over it, `ListModels` powers `talon models`. The wire formats differ —
OpenAI streams `choices[].delta`, Anthropic streams typed events with
`input_json_delta` for tool arguments, Gemini streams
`streamGenerateContent?alt=sse` with `functionCall` parts — but the agent only
ever sees `StreamEvent`s.

HTTP concerns live in `llm/client`: authentication, `Retry-After`, exponential
backoff, status-code mapping and SSE framing. A provider file is then mostly
translation.

## Filesystem safety

- `tools.Context.resolvePath` rejects absolute paths and `..` escapes.
- `read_file` refuses files above a configurable size instead of flooding the
  context.
- `edit_file` requires a unique match unless `replace_all` is set, and reports
  the ambiguity count otherwise.
- `apply_patch` verifies context lines *before* writing anything.
- Every mutation goes through `journal.Record`, which is what makes `/undo`
  reliable rather than best-effort.

## Shell execution

`internal/shell` puts each command in its own process group. A timeout or
cancellation kills the whole group, otherwise a background child keeps the pipes
open and `Run` blocks. Output is streamed to the UI and captured with a hard
byte limit, and the transcript records what was announced and what was dropped.

## Rendering

`internal/ui` is a pure function of content: themes are tables of SGR sequences,
the markdown renderer emits styled text, the spinner owns one line. The REPL
composes them. When output is not a terminal (a pipe, CI), the theme collapses to
`mono` and the spinner disables itself — Talon stays readable when it cannot be
pretty.

## Extensions

Plugins and MCP servers are the same thing from Talon's point of view: external
processes that answer `tools/list` and `tools/call` over JSON-RPC on stdio.
`internal/rpc` implements the transport; `internal/plugin` and `internal/mcp` add
the handshakes and the naming conventions. Both produce `tools.Definition`
values, so the permission policy, the UI and the session log treat external
tools exactly like built-in ones.

## Dependencies

There are none, on purpose:

- TOML: `internal/toml` implements the subset configuration files use.
- Terminal: `internal/term` implements raw mode and the line editor.
- Markdown and syntax highlighting: `internal/ui`.
- JSON-RPC: `internal/rpc`.

The cost is a few thousand lines we own; the benefit is a static binary that
builds offline, cross-compiles trivially, has no CVEs from transitive
dependencies, and behaves identically on every platform. If a future feature
genuinely needs a large dependency (a TUI framework, a database), that should be
an explicit, discussed decision.

## Testing strategy

| Layer | Approach |
|---|---|
| Parsers, policies, diffing, rendering | table-driven unit tests, no I/O |
| Providers | `httptest` servers replaying real SSE frames |
| Tools | real temp projects, real `git`, real processes |
| Plugins and MCP | a fake plugin compiled on the fly and spoken to over a real pipe |
| The whole product | `internal/integration` drives the REPL with a scripted provider |

No test requires an API key or network access.
