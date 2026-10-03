# Plugins

A Talon plugin is an executable that speaks newline-delimited JSON-RPC 2.0 on
stdin/stdout. Plugins can add tools; they cannot change the agent, the UI or the
permission policy. Everything a plugin does still passes through the normal
permission checks.

## Quick start

```bash
cd examples/plugins/hello
go build -o hello .
talon plugins test .        # run it, list its tools, then disconnect
talon plugins install .     # copy into ~/.local/share/talon/plugins/hello
talon plugins list
```

Inside a session the tools show up as `hello_greet` and `hello_now`:

```
❯ /tools
… hello_greet  read  plugin:hello  Greet someone.
```

## The manifest

`talon-plugin.json` in the plugin directory:

```json
{
  "name": "hello",
  "version": "1.0.0",
  "description": "Example plugin",
  "command": "./hello",
  "args": ["--stdio"],
  "env": { "HELLO_MODE": "quiet" },
  "tool_prefix": "hello",
  "risk": "read"
}
```

| Field | Meaning |
|---|---|
| `name` | required; the plugin's identifier |
| `version` | shown in `talon plugins list` |
| `description` | shown to the user |
| `command` | required; relative paths resolve inside the plugin directory |
| `args` | arguments passed to the command |
| `env` | extra environment variables |
| `tool_prefix` | prefix for exposed tools (defaults to `name`) |
| `risk` | `read`, `write`, `exec` or `danger` — applied to every tool |

`risk` matters: a plugin marked `read` runs without confirmation. Choose it
honestly, and prefer `read` for anything that only queries state.

## Protocol

Talon sends:

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocol":1,"client":"talon","capabilities":{"tools":true}}}
{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"greet","arguments":{"name":"Ada"}}}
```

The plugin answers:

```json
{"jsonrpc":"2.0","id":1,"result":{"protocol":1,"name":"hello","version":"1.0.0"}}
{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"greet","description":"…","inputSchema":{…}}]}}
{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"Good morning, Ada!"}],"isError":false}}
```

Rules:

- One JSON object per line, no embedded newlines, UTF-8.
- `protocol` must be `1`.
- Tools may be declared in the `initialize` result or served by `tools/list`.
- Use `isError` (or `is_error`) with a text content block to report a failure
  that the model should see as information.
- Log to **stderr**. stdout is the protocol channel; anything else on stdout
  corrupts it. Talon captures the last stderr lines and includes them in plugin
  errors, which makes them worth writing well.
- Handle `shutdown` by exiting.

## Installing from a URL

```bash
talon plugins install https://github.com/me/talon-docker
```

Talon clones the repository with `git clone --depth 1` and then runs only the
executable the manifest names. It never executes a downloaded script. Review the
repository first, and verify the plugin after installing:

```bash
talon plugins test ~/.local/share/talon/plugins/talon-docker
```

Plugins are as trusted as the code you let run in your repository. `full-access`
plus a `read` plugin means that plugin's commands run unattended.

## Writing a plugin

Any language with line-delimited JSON works. Keep the process alive for the whole
session, answer promptly, and return small payloads: tool output is truncated
before it reaches the model.

Talon's own `internal/rpc` package is a good reference for a Go plugin, and
`examples/plugins/hello` is about a hundred lines.

## MCP servers

MCP servers use the same transport and are configured separately:

```toml
[mcp]
enabled = true
[[mcp.servers]]
name = "filesystem"
command = "mcp-server-filesystem"
args = ["/home/me/projects"]
```

Talon performs the MCP `initialize` handshake, sends
`notifications/initialized`, and exposes `tools/list` results as
`<server>_<tool>` tools with `exec` risk. Both mechanisms can be used together.
