# Talon

**An agentic coding CLI for your terminal.**

Talon reads your project, plans changes, edits files, runs your build and tests,
reads the errors, fixes the cause and shows you exactly what changed — all from
a single prompt.

```console
$ talon
Talon
agentic coding CLI · openai/gpt-5 · ~/projects/talon
project: Go, 128 files, git main
tools: 19 · permissions: confirm · /help for commands

❯ the parser tests fail after the last refactor, fix them
```

```
· go test ./...: failed (exit 1)
✓ read internal/parser/parser.go (212 lines)
✓ edited internal/parser/parser.go +6 −4
✓ go test ./...: succeeded (exit 0)

The lexer dropped the last token when the input ended without a newline.
`parser.go:88` now handles EOF explicitly; the suite passes (42 tests).
```

---

## Contents

- [Why Talon](#why-talon)
- [Install](#install)
- [Configure](#configure)
- [Providers](#providers)
- [Using Talon](#using-talon)
- [Commands](#commands)
- [Slash commands](#slash-commands)
- [Tools](#tools)
- [Permissions](#permissions)
- [Plan mode](#plan-mode)
- [Sessions and memory](#sessions-and-memory)
- [Plugins and MCP](#plugins-and-mcp)
- [Configuration reference](#configuration-reference)
- [Security](#security)
- [Development](#development)
- [Architecture](#architecture)
- [Safety](#safety)
- [License](#license)

---

## Why Talon

Most AI coding tools stop at suggestions. Talon is an **agent**: it runs a
loop — understand, plan, act with tools, observe the result, reason, repeat —
until the task is done, and it works inside a real repository with your real
build system.

- **Real tools, not a chat box.** 19 built-in tools for reading, editing,
  searching, building, testing and Git. The model has no other access to your
  machine.
- **Every change is a patch.** Edits are shown as diffs and recorded, so `/undo`
  and `/diff` always work.
- **Permission levels you control.** Read-only, safe, confirm (default),
  full-access. Dangerous operations always ask.
- **Provider-agnostic.** OpenAI, Anthropic, Gemini, OpenRouter, Ollama,
  llama.cpp or any OpenAI-compatible gateway — configured, never compiled in.
- **Extensible.** Plugins and MCP servers add tools without touching Talon.
- **One static binary, zero runtime dependencies.** No npm, no Python, no
  containers. Go standard library only.

---

## Install

### One-line install

```bash
curl -fsSL https://raw.githubusercontent.com/CRISTOP-bot/talon/main/install.sh | sh
```

That pipes a remote script into a shell, which is exactly the pattern this
project warns against in its own documentation. If you would rather see what you
run, download it first — the result is identical:

```bash
curl -fsSL https://raw.githubusercontent.com/CRISTOP-bot/talon/main/install.sh -o install.sh
less install.sh
sh install.sh
```

What the script does, in order: detects OS and architecture, asks the GitHub
API for the latest release (or `$TALON_VERSION`), downloads the matching asset,
downloads `checksums.txt` from the same release, refuses to continue unless the
published SHA-256 matches, and only then installs into `/usr/local/bin` (or
`$TALON_INSTALL`, or `~/.local/bin` when not root). Nothing is executed before it
is verified, and a missing checksum is a hard failure, not a warning.

| Variable | Effect |
| --- | --- |
| `TALON_VERSION` | install a specific release tag instead of the latest |
| `TALON_INSTALL` | install directory (default `/usr/local/bin`, else `~/.local/bin`) |
| `TALON_REPO` | install from a fork: `TALON_REPO=user/talon sh install.sh` |
| `TALON_NOCHECK` | `1` skips checksum verification — not recommended |

Install into your home directory without root:

```bash
TALON_INSTALL="$HOME/.local/bin" sh install.sh
export PATH="$HOME/.local/bin:$PATH"   # add to your shell profile to persist
```

Supported targets: `linux` and `darwin` on `amd64` and `arm64`. Everything else
should be built from source (`go install ./cmd/talon`). The current limitation
is that releases are verified by checksum only; there is no signature
verification yet — see [docs/security-review.md](docs/security-review.md).

### Packages

Release builds attach a Debian package; the RPM and AppImage targets are in the
Makefile.

```bash
# Debian/Ubuntu (published with each release)
sudo apt install ./talon_0.1.0_linux_amd64.deb

# From source, locally
make deb        # dist/talon_0.1.0_linux_amd64.deb   (needs dpkg-deb)
make rpm        # needs rpmbuild
make appimage   # lays out dist/appimage; seal it with appimagetool
make cross      # static binaries for linux, darwin and windows
make checksums  # dist/checksums.txt, which `talon update` verifies against
```

### From source

Requires Go 1.24 or newer.

```bash
git clone https://github.com/CRISTOP-bot/talon.git
cd talon
make build          # builds ./bin/talon
make install        # installs into $(go env GOPATH)/bin
```

Or without cloning:

```bash
go install github.com/CRISTOP-bot/talon/cmd/talon@latest
```

### Verify the installation

```bash
talon version
talon doctor        # checks config, key, directories, git and terminal
```

---

## Configure

```bash
talon init --provider openai --model gpt-5
export AI_API_KEY=sk-...
talon doctor
```

Configuration lives in two files, both optional:

| File | Purpose |
|---|---|
| `~/.config/talon/config.toml` | your preferences, shared across projects |
| `<project>/.talon/config.toml` | project-specific settings (commit this) |

Environment variables override both:

```bash
export AI_PROVIDER=anthropic
export AI_MODEL=claude-sonnet-4-5
export AI_API_KEY=sk-ant-...
```

Talon never writes your API key to a log, and `config list` masks it unless you
explicitly pass `--show-secrets`.

---

## Providers

Every provider below works out of the box: Talon reads the key from the variable
named in the table, and its host is already in the network allowlist, so you do
not have to configure anything else.

| Provider | `model.provider` | API root | Key variable |
|---|---|---|---|
| OpenAI | `openai` | `https://api.openai.com/v1` | `OPENAI_API_KEY` |
| Anthropic | `anthropic` | `https://api.anthropic.com/v1` | `ANTHROPIC_API_KEY` |
| Google Gemini | `gemini` | `https://generativelanguage.googleapis.com/v1beta` | `GEMINI_API_KEY` |
| OpenRouter | `openrouter` | `https://openrouter.ai/api/v1` | `OPENROUTER_API_KEY` |
| NVIDIA | `nvidia` | `https://integrate.api.nvidia.com/v1` | `NVIDIA_API_KEY` |
| Groq | `groq` | `https://api.groq.com/openai/v1` | `GROQ_API_KEY` |
| Together | `together` | `https://api.together.xyz/v1` | `TOGETHER_API_KEY` |
| DeepSeek | `deepseek` | `https://api.deepseek.com/v1` | `DEEPSEEK_API_KEY` |
| Mistral | `mistral` | `https://api.mistral.ai/v1` | `MISTRAL_API_KEY` |
| Fireworks | `fireworks` | `https://api.fireworks.ai/inference/v1` | `FIREWORKS_API_KEY` |
| Cerebras | `cerebras` | `https://api.cerebras.ai/v1` | `CEREBRAS_API_KEY` |
| xAI | `xai` | `https://api.x.ai/v1` | `XAI_API_KEY` |
| Perplexity | `perplexity` | `https://api.perplexity.ai` | `PERPLEXITY_API_KEY` |
| SiliconFlow | `siliconflow` | `https://api.siliconflow.cn/v1` | `SILICONFLOW_API_KEY` |
| Hugging Face | `huggingface` | `https://router.huggingface.co/v1` | `HF_TOKEN` |
| GitHub Models | `github` | `https://models.github.ai/inference` | `GITHUB_TOKEN` |
| Ollama (local) | `ollama` | `http://localhost:11434/v1` | none |
| llama.cpp (local) | `llamacpp` | `http://localhost:8080/v1` | none |
| LM Studio (local) | `lmstudio` | `http://localhost:1234/v1` | none |
| Any OpenAI-compatible server | `custom` | your `model.base_url` | optional |

`AI_API_KEY` works as a fallback for any of them, and `model.api_key_env`
overrides the variable when you need a different one.

Switching provider takes one command and no other change:

```bash
talon config set model.provider nvidia
talon config set model.name qwen/qwen3-coder-480b-a35b-instruct
export NVIDIA_API_KEY=nvapi-...
talon doctor        # tells you exactly which variable it is reading
```

```bash
# Local models, no key required
talon config set model.provider ollama
talon config set model.name qwen2.5-coder:7b
talon models

# A self-hosted gateway
talon config set model.provider custom
talon config set model.base_url http://localhost:11434/v1
```

`model.provider` values that Talon does not know are treated as OpenAI-compatible
if `model.base_url` points at something that speaks that protocol.

---

## The interface

Talon opens a full-screen interface when it runs on a terminal, and falls back
to a line editor when it does not (pipes, CI, logs). Force the line editor with
`--no-tui`.

```
┌────────────────────────────────────────────────────────────────────────────┐
│ talon 0.1.0                              anthropic/claude-sonnet · confirm │
├────────────────────────────────────────────────────────────────────────────┤
│                                                                            │
│  you   refactor el parser para que tolere CRLF                            │
│                                                                            │
│        Hecho. Cambié internal/parser.go para normalizar los finales de    │
│        línea antes de tokenizar, y añadí un caso de prueba.                │
│                                                                            │
│  • edit_file internal/parser.go                                            │
│  • run_command go test ./...                                               │
│                                                                            │
├────────────────────────────────────────────────────────────────────────────┤
│ ❯ revisa el parser y dime si falta algo @internal/parser.go                │
│ tab plan/build · @ file · / command · ctrl+l clear · ctrl+c exit           │
│                                                            plan           │
└────────────────────────────────────────────────────────────────────────────┘
```

| Key | What it does |
| --- | --- |
| type | write the request; every keystroke is rendered as you press it |
| `Tab` | switch between **build** and **plan** mode, shown at the lower right |
| `@` | fuzzy file picker; `Enter` inserts `@path` into the request |
| `/` | command palette over every slash command |
| `Enter` | send the request |
| `Ctrl+C` | clear the request; interrupt a running turn; leave when empty |
| `Ctrl+D` | leave when the request is empty |
| `Ctrl+L` | clear the conversation |
| `↑` `↓` | walk the history |

In **plan** mode the agent reads and proposes but changes nothing, exactly like
`--plan`. The permission level, the sandbox and every security control apply
identically in both interfaces: the interface is a view, not a policy.

## Using Talon

### Interactive

```bash
cd ~/projects/my-app
talon
```

### One-shot

```bash
talon "add tests for the parser and run them"
talon --plan "how should I split this module?"   # plan only, changes nothing
talon --yes "regenerate the API client"           # unattended; still limited by permissions
```

### In scripts

```bash
talon --non-interactive "list every TODO with a file and line number" < /dev/null
```

---

## Commands

```
talon                     start an interactive session
talon "<prompt>"          run one prompt and exit
talon init                write a starter configuration file
talon config list|get|set|unset|path
talon doctor              verify the environment (config, key, dirs, git, terminal)
talon models              list provider models; --set <id> to select one
talon tools               list the tools the agent can use
talon permissions         show or change the permission level
talon sessions            list, show and delete saved sessions
talon terms               read the Terms of Use; accept, diff, history
talon privacy             the Privacy Notice; list and clear stored data
talon security            the security controls in force for this directory
talon sandbox             the kernel isolation available for commands
talon audit               the local security audit log
talon data | history      inspect or delete what Talon stores
talon plugins             install, test and remove plugins
talon update              update Talon (checksums verified); --check to look only
talon version             version and build information
```

Session flags: `--yes`, `--plan`, `--model`, `--privacy`, `--no-sandbox`,
`--accept-terms`, `--audit-level`, `--no-tui`, `--non-interactive`,
`--append-system`, `--no-index`, `--no-color`, `--debug`.

Global flags: `--cwd`, `--no-color`, `--debug`, `--verbose`, `--quiet`,
`--log-file`, `--version`.

---

## Slash commands

| Command | What it does |
|---|---|
| `/help` | list the commands |
| `/plan <request>` | produce a plan without changing anything |
| `/plan run` | execute the last plan step by step |
| `/mode [ask\|auto]` | show or set the autonomy mode |
| `/permissions [level]` | show or change the permission level |
| `/tools [suggest <request>]` | list tools, or suggest which ones fit a request |
| `/context` | show what is sent to the model (tokens, index, prompt) |
| `/diff` | show the changes made in this session |
| `/undo`, `/redo` | revert or reapply the last change |
| `/session list\|load\|save\|delete` | manage saved conversations |
| `/compact` | summarise the conversation to free context |
| `/memory list\|add\|forget` | manage remembered notes |
| `/index` | rebuild the project index |
| `/git` | git status plus the current diff |
| `/security` | the security controls in force |
| `/sandbox` | the kernel isolation available here |
| `/terms [accept\|history]` | read the Terms and record acceptance |
| `/privacy` | the Privacy Notice and the data stored |
| `/audit [n]` | recent entries from the local audit log |
| `/data [list\|clear <category>]` | inspect or delete stored data |
| `/clear`, `/exit` | start over, leave |

With `--no-tui` the line editor is used instead, with these keys: `Ctrl+C`
interrupts the turn, `Ctrl+D` exits, `↑`/`↓` recall history, `Ctrl+R` searches it,
`Tab` completes commands and paths, `Ctrl+A/E/K/U/W` edit the line, `Ctrl+L`
clears the screen.

---

## Tools

| Tool | Risk | Purpose |
|---|---|---|
| `read_file` | read | read a file with line numbers, optional range |
| `write_file` | write | create or replace a file |
| `edit_file` | write | replace an exact string (requires a unique match) |
| `apply_patch` | write | apply a unified diff with context verification |
| `delete_file` | danger | delete a file or directory (always confirms) |
| `list_directory` | read | list entries, optionally as a tree |
| `search_files` | read | find files by glob or substring |
| `search_text` | read | search content, plain or regex |
| `list_symbols` | read | list declarations in a file without reading it |
| `inspect_project` | read | project overview: language, build, tests, git |
| `run_command` | exec | run a shell command and return its output |
| `run_tests` | exec | run the detected test suite |
| `build_project` | exec | run the detected build |
| `git_status`, `git_diff`, `git_log`, `git_branch` | read | inspect the repository |
| `git_commit` | write | stage paths and create a local commit |
| `git_checkout` | exec | switch or create a branch |

There is deliberately **no push tool**: Talon never pushes to a remote.

Plugins and MCP servers can add more tools; they appear in `talon tools` with
their source (`plugin:<name>`, `mcp:<name>`).

---

## Permissions

```bash
talon permissions              # show the effective policy
/permissions safe              # change it for this session
talon permissions full-access --save
```

| Level | Reads | Writes in the project | Commands | Destructive |
|---|---|---|---|---|
| `read-only` | auto | denied | denied | denied |
| `safe` | auto | auto | asks | asks |
| `confirm` (default) | auto | asks | asks | asks |
| `full-access` | auto | auto | auto | asks |

Deny lists (`permissions.deny_commands`, `permissions.deny_paths`,
`permissions.deny_tools`) are enforced at every level, including
`full-access`. Paths outside the project are denied unless explicitly allowed.

When an operation needs confirmation:

```
⚠ This action needs your confirmation
  Edit src/parser.go
  tool         edit_file
  risk         write

  [y] once   [a] always allow this tool   [n] reject
```

`y`/`a`/`n` work; anything else is sent to the model as the rejection reason.

---

## Plan mode

```bash
talon --plan "how should I split the billing module?"
/plan add OAuth support
```

Plan mode exposes **only read-only tools**: the model can inspect the project,
but it cannot write files, run commands or commit. It finishes with a numbered
plan; `/plan run` executes the steps one at a time, so you approve before each
one runs.

---

## Sessions and memory

Every turn is written to a session file, so you can resume later:

```bash
talon sessions list
talon sessions show 20260104-101533-ab12
```

```
/session list
/session load 20260104-101533-ab12
```

Memory holds what Talon should know about a project between sessions —
architecture, conventions, commands, known errors:

```
/memory list
/memory add test-command "cargo test --all-features"
/memory forget test-command
```

Notes are stored per project path under `~/.local/share/talon/memory/`, and
anything you want to commit lives in `<project>/.talon/memory.md` (also read:
`TALON.md`, `MEMORY.md`, `AGENTS.md`). Notes that look like credentials are
rejected.

---

## Plugins and MCP

A plugin is any executable that speaks newline-delimited JSON-RPC 2.0 on
stdin/stdout:

```bash
talon plugins test ./my-plugin      # run it without installing
talon plugins install ./my-plugin   # copy into the plugin directory
talon plugins install https://github.com/me/talon-docker
talon plugins list
talon plugins remove my-plugin
```

`examples/plugins/hello` is a complete, buildable example. See
[docs/plugins.md](docs/plugins.md).

MCP servers are configured in TOML and become tools:

```toml
[mcp]
enabled = true

[[mcp.servers]]
name = "filesystem"
command = "mcp-server-filesystem"
args = ["/home/me/projects"]
```

---

## Configuration reference

Full reference: [docs/configuration.md](docs/configuration.md).

```toml
model.provider   = "openai"     # see the provider table above
model.name       = "gpt-5"
model.base_url   = ""           # empty means the provider default
model.api_key_env = "AI_API_KEY"
model.temperature = 0.2
model.max_tokens  = 8192
model.timeout_seconds = 120
model.max_retries = 3

[agent]
max_steps = 24                  # tool-call rounds per request
max_tool_output = 20000         # bytes of tool output sent to the model

[context]
auto_compact = true
compact_at = 0.75

[ui]
theme = "default"               # default | mono
spinner = true
stream = true

[permissions]
level = "confirm"
deny_commands = ["git push --force"]

[sessions]
enabled = true
autosave = true
max = 200

[memory]
enabled = true

[security]
command_confirmation = true    # ask before running a command
sandbox = true                 # kernel isolation where available
sandbox_required = false       # refuse commands if no kernel sandbox exists
network_mode = "allowlist"     # allowlist | ask | off
network_confirmation = true    # ask before contacting an unlisted host
allowed_domains = []           # extra hosts the agent may reach
allow_http = false             # plain http (needed by some local servers)
allow_loopback = false         # 127.0.0.0/8 (auto-enabled for local providers)
allow_private_ips = false      # RFC1918 and friends
secret_redaction = true        # scrub credentials from logs, errors and prompts
sensitive_files = "block"      # block | mask | warn | allow
content_findings = "mask"      # credentials found in ordinary content
allow_sensitive_paths = []     # credential files to permit explicitly
audit_enabled = true
audit_level = "normal"         # minimal | normal | verbose | off
privacy_mode = false           # keep nothing on disk
require_terms = true           # ask for acceptance after a material change
max_file_bytes = 524288        # refuse files larger than this
telemetry = false              # this build has no telemetry; cannot be enabled

[log]
level = "warn"                  # debug | info | warn | error | off
```

---

## Development

```bash
make help          # every target
make build         # build ./bin/talon with version stamping
make test          # unit + integration tests (no network needed)
make test-race     # the suite under the race detector
make lint          # gofmt check and go vet
make run ARGS="--" # run from source
make cross         # build for linux, darwin and windows
make clean
```

The integration suite runs the real REPL against a scripted provider, real
tools and a real plugin process, so nothing in the test suite needs an API key.

See [CONTRIBUTING.md](CONTRIBUTING.md) for the conventions and
[docs/architecture.md](docs/architecture.md) for the design.

---

## Architecture

```
cmd/talon              entry point
internal/cli           argument parsing, command dispatch, exit codes
internal/commands      version, init, config, doctor, models, tools,
                       permissions, sessions, plugins, update
internal/repl          interactive loop, slash commands, confirmations
internal/agent         the agent loop: context → model → tools → observe
internal/llm           provider interface: OpenAI, Anthropic, Gemini, mock
internal/tools         tool definitions, registry, validation, execution
internal/perm          permission levels and decisions
internal/context       system prompt and project snapshot
internal/project       language/framework/build detection, project walking
internal/index         file index, text search, symbol search, related files
internal/shell         command execution with timeouts and streaming
internal/git           typed git wrapper (no push, ever)
internal/journal       undo/redo history of file changes
internal/session       conversation persistence
internal/memory        per-project notes
internal/plugin        plugin host (JSON-RPC over stdio)
internal/mcp           Model Context Protocol client
internal/rpc           shared JSON-RPC 2.0 client
internal/ui            themes, markdown, highlighting, diffs, spinner
internal/term          raw mode, key decoding, line editor, history
internal/toml          in-tree TOML parser/encoder
internal/diff          unified diff computation and parsing
internal/logger        redacted debug logging
internal/paths         XDG directories
internal/errs          error taxonomy and human-readable messages
```

Every boundary is an interface, so components can be replaced (another provider,
another shell, another storage backend) without touching the rest.

---

## Security

Defaults are the restrictive ones; `talon security` prints what is in force.

- **Sensitive data.** Credential files (`.env`, `~/.ssh`, `*.pem`,
  `.git-credentials`, cloud service-account keys…) are refused before they can be
  read; credentials found inside ordinary files are masked before the content is
  sent.
- **Untrusted content.** File contents and command output are wrapped as data
  with `authority=none`, so instructions inside a repository are quoted rather
  than obeyed. Matches are reported, not silently dropped.
- **Network.** One policy mediates all provider traffic: HTTPS only by default,
  an allowlist of hosts, cloud metadata endpoints always blocked, and
  loopback/private addresses refused unless enabled or required by a local model
  provider.
- **Sandbox.** On Linux with Landlock, commands run in a kernel-confined helper
  process with `no_new_privs` set. `talon sandbox` reports the truth on every
  platform; `security.sandbox_required = true` refuses commands when no kernel
  sandbox exists. Child processes do not inherit your credentials.
- **Audit.** Decisions are appended to a local JSONL log with `0600`
  permissions and redacted before writing (`talon audit`).
- **Privacy.** `--privacy` disables sessions, memory, logs and the audit log.
  `talon data` lists everything stored; `talon data clear <category>` deletes it
  after confirmation. There is no telemetry in this build.
- **Consent.** Terms and Privacy Notice versions are embedded in the binary;
  acceptance is recorded only on an explicit act, and only a material change
  asks again.

Known limits are stated in [docs/security-review.md](docs/security-review.md);
the model of what is defended and from what is in
[docs/security-model.md](docs/security-model.md), and approvals in
[docs/permissions.md](docs/permissions.md).

Destructive operations always require confirmation, the model cannot run
anything except registered tools, paths outside the project are denied unless
allowed by configuration, `git push` has no tool, and `talon update` verifies the
published SHA-256 checksums and keeps the previous binary as `<name>.old`.

Report security issues privately: see [SECURITY.md](SECURITY.md).

---

## License

MIT — see [LICENSE](LICENSE).