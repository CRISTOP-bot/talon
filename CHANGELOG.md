# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and Talon uses
[semantic versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

**Provider catalogue**
- One catalogue (`internal/llm/providers.go`) now defines every provider: its
  API root, the environment variables that hold its key, the protocol it speaks
  and the models it offers. Adding a provider there is enough to make it
  selectable, allowlisted by the network policy, resolvable for a key and
  listed by `talon models`.
- New providers: NVIDIA, Groq, Together, DeepSeek, Mistral, Fireworks, Cerebras,
  xAI, Perplexity, SiliconFlow, Hugging Face, GitHub Models and LM Studio,
  alongside the existing OpenAI, Anthropic, Gemini, OpenRouter, Ollama and
  llama.cpp.
- `talon doctor` now names the variable it actually reads for the configured
  provider, so the fix line is copy-pasteable.
- `model.provider` is validated against the catalogue, with a hint to run
  `talon models`.

### Added

**Full-screen interface (opencode-style)**
- `internal/tui`: a screen buffer with a difference renderer, the alternate
  screen, resize handling and a palette; only changed cells are written, so
  typing stays responsive.
- `internal/tuiapp`: the session view: header, scrolling conversation, editor,
  footer with the mode indicator, and overlays.
- `Tab` switches between build and plan mode with the indicator at the lower
  right; `@` opens a fuzzy file picker that inserts `@path`; `/` opens a command
  palette over the existing slash commands.
- Confirmations are shown inside the conversation instead of on a separate
  screen, and every existing slash command works unchanged: the printer's output
  is captured and turned into conversation blocks.
- `--no-tui` keeps the line editor, which is what pipes and CI use.

**Interactive input**
- The line editor no longer ignores the keyboard: it allocated a zero-length read
  buffer, so every read returned immediately and the prompt never received a
  keystroke.
- Raw mode now clears `ISIG`, so `Ctrl+C` reaches the application instead of
  being turned into `SIGINT` by the terminal.

### Fixed

**Security layer**
- `internal/secure`: one assembled posture built from configuration and wired
  into the tools, the shell runner, the provider client and the REPL.
- `internal/sensitive`: credential files are refused before being read;
  credentials inside ordinary content are masked before it is sent
  (`security.sensitive_files`, `security.content_findings`).
- `internal/netguard`: a single `RoundTripper` for all provider traffic — HTTPS
  by default, host allowlist, cloud metadata endpoints always blocked,
  loopback/private addresses refused unless enabled or needed by a local model
  provider, and request-body redaction.
- `internal/injection`: external content is wrapped as untrusted data with
  `authority=none`; instruction-override, exfiltration, remote-execution and
  delimiter-escape patterns are reported.
- `internal/sandbox`: Landlock confinement through a re-executed helper with
  `no_new_privs`, an honest status report where no kernel sandbox exists, and
  `security.sandbox_required` to refuse commands instead of degrading.
- `internal/audit`: append-only JSONL audit log (`0600`) with `minimal`,
  `normal` and `verbose` levels and redaction before writing.
- `internal/legal`: versioned Terms (v1.0.0, v1.1.0) and Privacy Notice
  (v1.0.0) embedded in the binary, with a local append-only consent ledger that
  only asks again after a material change.
- `internal/privacy`: honest privacy mode, a data inventory and confirmed,
  planned deletion of every stored category.
- New commands: `talon terms`, `talon privacy`, `talon security`, `talon
  sandbox`, `talon audit`, `talon data`, `talon history`.
- New session flags: `--privacy`, `--no-sandbox`, `--accept-terms`,
  `--audit-level`; `--accept-terms` is refused together with `--privacy`.
- New `[security]` configuration section, validated with restrictive defaults.
- Documentation: `SECURITY.md` rewritten, `PRIVACY.md`, `TERMS.md`,
  `docs/security-model.md`, `docs/permissions.md`, `docs/security-review.md`,
  `docs/verification.md`.
- Adversarial tests for prompt injection, secret leakage into content, search
  results and the audit log, path and size gates, network policy defaults,
  consent and privacy mode.

- Every named colour in the interface palette was shifted by one (an `iota`
  offset), so the accent colour rendered as magenta.
- A pty master does not support read deadlines on Linux, so the interface tests
  read through a goroutine.

### Fixed
- Session flags placed before a prompt (for example `talon --yes "…"`) are no
  longer rejected as unknown global flags.

## [0.1.0] — 2026-10-03

The first working release. Everything below is implemented and tested; there
are no placeholder features.

### Added

**Agent**
- Multi-step agent loop: understand → plan → select tools → execute → observe →
  reason → repeat, with a configurable step budget (`agent.max_steps`).
- Streaming responses with incremental markdown rendering; only actions, results
  and explanations are shown, never private model reasoning.
- Plan mode (`--plan`, `/plan`) that exposes read-only tools only, and `/plan
  run` to execute the resulting plan step by step.
- Autonomy modes: `ask`, `auto` and `plan`, mapped from the permission level.
- Conversation compaction (`/compact`, `context.auto_compact`).

**Providers**
- OpenAI, Anthropic, Google Gemini, OpenRouter, Ollama, llama.cpp and any
  OpenAI-compatible endpoint, plus a deterministic offline provider used by the
  test suite.
- Server-sent-event streaming with tool-calling for all three wire protocols.
- Error mapping for 401/403, 404, 429, 4xx, 5xx, timeouts, cancellation and
  malformed responses, with bounded retries and exponential backoff.

**Tools (19 built in)**
- Filesystem: `read_file`, `write_file`, `edit_file`, `apply_patch`,
  `delete_file`, `list_directory`, `search_files`, `search_text`,
  `list_symbols`.
- Shell: `run_command`, `run_tests`, `build_project`.
- Git: `git_status`, `git_diff`, `git_log`, `git_branch`, `git_commit`,
  `git_checkout`. No push tool exists.
- Project: `inspect_project`.
- JSON-schema validation of every argument set, output truncation, and a
  per-tool risk classification.

**Project understanding**
- Detection of 19 languages plus frameworks, package managers, build and test
  commands, entry points, test layouts and git state.
- Project index with `.gitignore` support, ignored-directory rules, symbol
  scanning, substring and regex search, glob file search and related-file
  suggestions.

**Safety**
- Four permission levels (`read-only`, `safe`, `confirm`, `full-access`) with
  deny lists enforced at every level, path containment and confirmations with
  `once` / `always` / `reject` answers.
- Redacted debug logging; credentials are never written to logs or memory.

**Editing and review**
- Unified diff computation and a git-patch parser for the diff viewer.
- Undo/redo journal for every file change (`/undo`, `/redo`, `/diff`).

**Interface**
- Line editor with history, incremental search, completion, readline bindings
  and multi-line continuation.
- Markdown renderer, syntax highlighter, coloured diffs, panels, tables and an
  animated spinner that degrades gracefully when output is not a terminal.
- Slash commands: `/help`, `/plan`, `/mode`, `/model`, `/models`, `/context`,
  `/tools`, `/permissions`, `/session`, `/diff`, `/undo`, `/redo`, `/compact`,
  `/memory`, `/index`, `/git`, `/clear`, `/exit`.

**Extensibility**
- Plugin host speaking newline-delimited JSON-RPC 2.0 over stdio, with a
  manifest, risk classification and per-plugin tool prefixes.
- MCP client (stdio transport) exposing server tools as Talon tools.
- `talon plugins test|install|list|remove`, with a complete example plugin.

**Operations**
- `talon init`, `config`, `doctor`, `models`, `tools`, `permissions`,
  `sessions`, `plugins`, `update`, `version`.
- Session persistence with resume, delete and pruning.
- Per-project memory notes plus committed memory files (`.talon/memory.md`).
- Checksum-verified self-update.
- Cross-platform raw mode, one static binary, zero runtime dependencies.
