# Configuration reference

Talon merges configuration in this order, later wins:

1. built-in defaults
2. `~/.config/talon/config.toml` (or `$TALON_CONFIG_DIR/config.toml`)
3. `<project>/.talon/config.toml`
4. environment variables

Every key is optional; `talon config list` shows the effective values.

## Files and directories

| Path | Contents |
|---|---|
| `~/.config/talon/config.toml` | user configuration (mode 0600) |
| `<project>/.talon/config.toml` | project configuration |
| `<project>/.talon/memory.md` | memory committed with the repository |
| `~/.local/share/talon/sessions/` | saved conversations |
| `~/.local/share/talon/memory/` | per-project notes |
| `~/.local/share/talon/plugins/` | installed plugins |
| `~/.local/state/talon/logs/talon.log` | debug logs |
| `~/.local/cache/talon/journal/` | undo journals |

Overridable with `TALON_CONFIG_DIR`, `TALON_DATA_DIR`, `TALON_STATE_DIR`,
`TALON_CACHE_DIR` and `TALON_HOME`; otherwise the XDG variables apply.

## Model

| Key | Default | Meaning |
|---|---|---|
| `model.provider` | `openai` | `openai`, `anthropic`, `gemini`, `openrouter`, `ollama`, `llamacpp`, `custom`, or any OpenAI-compatible name |
| `model.name` | — | model id passed to the provider |
| `model.base_url` | provider default | overrides the API root |
| `model.api_key` | — | key stored in the file (0600); prefer the environment |
| `model.api_key_env` | `AI_API_KEY` | variable the key is read from |
| `model.temperature` | `0.2` | 0–2; providers that do not support it ignore it |
| `model.top_p` | `1.0` | nucleus sampling |
| `model.max_tokens` | `8192` | output budget per request |
| `model.timeout_seconds` | `120` | per-request deadline |
| `model.max_retries` | `3` | retries for 429/5xx/network errors |
| `model.extra_headers.*` | — | extra HTTP headers, e.g. for gateways |

### Environment variables

| Variable | Effect |
|---|---|
| `AI_PROVIDER` | `model.provider` |
| `AI_MODEL` | `model.name`; `provider/name` sets both unless `AI_PROVIDER` is set |
| `AI_BASE_URL` | `model.base_url` |
| `AI_API_KEY` | the API key (with `ANTHROPIC_API_KEY`, `GEMINI_API_KEY` and `OPENAI_API_KEY` as fallbacks) |
| `AI_TEMPERATURE`, `AI_MAX_TOKENS` | sampling and output budget |
| `TALON_PERMISSIONS` | permission level (`ask`, `confirm`, `safe`, `full-access`, `read-only`) |
| `TALON_THEME` | `default` or `mono` |
| `TALON_LOG_LEVEL` | `debug`, `info`, `warn`, `error`, `off` |
| `TALON_MAX_STEPS` | agent step budget |
| `TALON_SESSION_ID` | reuse a session id instead of generating one |
| `TALON_UPDATE_API` | alternative release API base (used by tests) |

## Agent

| Key | Default | Meaning |
|---|---|---|
| `agent.max_steps` | `24` | tool-call rounds per request |
| `agent.max_tool_output` | `20000` | bytes of tool output sent to the model |
| `agent.auto_approve_tools` | `[]` | tools that always ask, even in full-access |
| `agent.system_prompt_file` | — | extra system instructions from a file |

## Context

| Key | Default | Meaning |
|---|---|---|
| `context.max_bytes` | `180000` | soft cap for the initial context block |
| `context.max_files` | `60` | cap for file listings |
| `context.max_file_bytes` | `400000` | files above this are not inlined |
| `context.respect_gitignore` | `true` | honour `.gitignore` when walking |
| `context.include_git_status` | `true` | include the git state in the prompt |
| `context.auto_compact` | `true` | summarise the conversation when it grows |
| `context.compact_at` | `0.75` | fraction of the context window that triggers it |

## Interface

| Key | Default | Meaning |
|---|---|---|
| `ui.theme` | `default` | `default` (colours) or `mono` |
| `ui.spinner` | `true` | animated activity line (auto-disabled off-terminal) |
| `ui.stream` | `true` | stream shell output while it runs |
| `ui.show_thinking` | `true` | reserved for status text; private model reasoning is never printed |
| `ui.syntax` | `true` | syntax highlighting in code blocks |

## Permissions

| Key | Default | Meaning |
|---|---|---|
| `permissions.level` | `confirm` | `read-only`, `safe`, `confirm`, `full-access` (aliases: `interactive`/`ask` → confirm, `auto` → full-access) |
| `permissions.deny_commands` | a small baseline list | substrings that are always refused |
| `permissions.allow_commands` | `[]` | substrings that skip confirmation |
| `permissions.deny_paths` | `[]` | glob patterns that are always refused |
| `permissions.allow_paths` | `[]` | glob patterns allowed outside the project |
| `permissions.timeout_seconds` | `600` | command timeout |

## Memory, sessions, logging

| Key | Default | Meaning |
|---|---|---|
| `memory.enabled` | `true` | per-project notes |
| `memory.max_notes` | `200` | notes kept per project |
| `sessions.enabled` | `true` | conversation persistence |
| `sessions.autosave` | `true` | write after every turn |
| `sessions.max` | `200` | sessions kept per project |
| `log.level` | `warn` | `debug`, `info`, `warn`, `error`, `off` |
| `log.file` | `~/.local/state/talon/logs/talon.log` | log destination |
| `log.redact` | `true` | scrub credentials from log lines |

## Plugins

```toml
[plugins]
enabled = true
dir = "~/.local/share/talon/plugins"

[[plugins.entries]]
name = "docker"
description = "Docker helpers"
command = "./docker-plugin"
args = []
tool_prefix = "docker"
risk = "read"          # read | write | exec | danger
enabled = true

[plugins.entries.env]
DOCKER_HOST = "unix:///var/run/docker.sock"
```

Plugin directories are auto-discovered: any directory under `dir` containing
`talon-plugin.json` is loaded. `plugins.entries` exists for plugins you start
manually.

## MCP servers

```toml
[mcp]
enabled = true

[[mcp.servers]]
name = "filesystem"
command = "mcp-server-filesystem"
args = ["/home/me/projects"]
enabled = true

[mcp.servers.env]
LOG_LEVEL = "error"
```

Tools are exposed as `<server>_<tool>`, classified as `exec`, and therefore
subject to the normal permission policy.

## Updates

| Key | Default | Meaning |
|---|---|---|
| `update.repo` | `CRISTOP-bot/talon` | release repository |
| `update.channel` | `stable` | informational; `talon update --version <tag>` selects explicitly |
| `update.check_on_start` | `false` | reserved for future opt-in checks |

## Examples

### Local-only setup

```toml
model.provider = "ollama"
model.name     = "qwen2.5-coder:7b"
model.temperature = 0.1

[permissions]
level = "safe"

[ui]
theme = "mono"
```

### Strict review workflow

```toml
model.provider = "anthropic"
model.name = "claude-sonnet-4-5"

[permissions]
level = "confirm"
deny_commands = ["git push", "gh pr merge", "npm publish"]

[agent]
max_steps = 30
```

### Shared project defaults (committed)

`<project>/.talon/config.toml`:

```toml
model.name = "gpt-5"
agent.system_prompt_file = "docs/agent-conventions.md"

[permissions]
level = "safe"
deny_paths = [".env", "*.pem", "secrets/**"]
```

## `[security]`

The security posture is resolved once at startup from this section. Every key
defaults to the restrictive value, and `talon security` prints what is in force,
so a weakened setting is always visible.

| Key | Default | Meaning |
| --- | --- | --- |
| `command_confirmation` | `true` | Ask before running a command |
| `sandbox` | `true` | Use kernel isolation where the platform provides it |
| `sandbox_required` | `false` | Refuse commands when no kernel sandbox exists instead of degrading |
| `network_mode` | `"allowlist"` | `allowlist`, `ask` or `off` |
| `network_confirmation` | `true` | Ask before contacting a host outside the allow list |
| `allowed_domains` | `[]` | Extra hosts Talon may contact |
| `allow_http` | `false` | Permit plain `http` |
| `allow_loopback` | `false` | Permit `127.0.0.0/8`; set automatically for local providers |
| `allow_private_ips` | `false` | Permit private and link-local addresses; set automatically for local providers |
| `secret_redaction` | `true` | Scrub credentials from logs, errors, prompts and audit records |
| `sensitive_files` | `"block"` | `block`, `mask`, `warn` or `allow` for credential files |
| `content_findings` | `"mask"` | Same set, for credentials found inside ordinary content |
| `allow_sensitive_paths` | `[]` | Credential files to permit explicitly |
| `audit_enabled` | `true` | Record security decisions locally |
| `audit_level` | `"normal"` | `minimal`, `normal`, `verbose` or `off` |
| `audit_path` | platform path | Override the audit log location |
| `privacy_mode` | `false` | Keep nothing on disk |
| `require_terms` | `true` | Ask for acceptance after a material Terms change |
| `max_file_bytes` | `524288` | Refuse files larger than this before they enter a prompt |
| `telemetry` | `false` | This build contains no telemetry; enabling it is a configuration error |

Examples:

```toml
# Local model server, nothing else on the network.
[security]
network_mode = "allowlist"
allowed_domains = []

# Strict review environment.
[security]
sensitive_files = "block"
content_findings = "block"
network_mode = "ask"
audit_level = "verbose"
sandbox_required = true

# Shared project defaults (committed)
[security]
allowed_domains = ["internal.example.com"]
```

Note that a project file can lower these values. Talon takes the resolved
configuration as given, so treat a committed `.talon/config.toml` as part of the
repository's trust boundary and read `talon security` when reviewing one.
