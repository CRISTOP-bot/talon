# Development guide

## Prerequisites

- Go 1.24 or newer
- `git`
- nothing else: there are no third-party Go modules

```bash
git clone https://github.com/talon-cli/talon
cd talon
make help
make build
make test
```

## Make targets

| Target | What it does |
|---|---|
| `make build` | builds `./bin/talon` with version, commit and date stamped in |
| `make install` | installs into `$(go env GOPATH)/bin` |
| `make test` | `go test ./...` |
| `make test-race` | the suite under `-race` |
| `make cover` | coverage summary per package |
| `make lint` | `gofmt -l` plus `go vet` |
| `make run ARGS="…"` | run from source |
| `make cross` | build for linux, darwin and windows (amd64 and arm64) |
| `make clean` | remove build output |

## Layout

Each package owns one concern and is usable on its own:

```
cmd/talon          entry point: build the app, register commands, exit
internal/cli       dispatch, flags, help, exit codes
internal/commands  one file per verb
internal/repl      the interactive session
internal/agent     the loop, conversation, plans, compaction
internal/llm       providers and the shared HTTP/SSE layer
internal/tools     tool definitions, registry, validation
internal/perm      the permission decision function
internal/context   system prompt and project snapshot
internal/project   language/framework detection and safe walking
internal/index     file index, search, symbols, related files
internal/shell     command execution
internal/git       typed git wrapper
internal/journal   undo/redo
internal/session   conversation persistence
internal/memory    per-project notes
internal/plugin    plugin host
internal/mcp       MCP client
internal/rpc       JSON-RPC 2.0 client
internal/ui        themes, markdown, highlighting, diffs, spinner
internal/term      raw mode, keys, line editor, history
internal/toml      TOML subset parser and encoder
internal/diff      unified diff computation and parsing
internal/logger    redacted logging
internal/paths     XDG directories
internal/errs      error taxonomy
internal/updater   verified self-update
```

## Adding things

- **Tool** → see [tools.md](tools.md#adding-a-tool).
- **Provider** → see [providers.md](providers.md#adding-a-provider).
- **Command** → new file in `internal/commands`, register it in
  `commands.go`. Set `Name`, `Summary`, `Detailed` and `Setup`.
- **Slash command** → add an entry to `slashDescriptions` in
  `internal/repl/commands.go` and a case in `handleSlash`.
- **Theme** → extend `ui.Theme` and `ui.ThemeByName`.

## Debugging

```bash
talon --debug                       # logs to ~/.local/state/talon/logs/talon.log
talon --debug --log-file /tmp/t.log # or wherever you want
talon doctor                        # environment sanity check
talon /context                      # what the model is being told
```

Logs are redacted: credentials are masked before anything is written. Request
bodies are logged at debug level so you can see exactly what was sent, including
tool schemas.

### Testing a provider without spending tokens

```bash
AI_PROVIDER=mock AI_MODEL=mock-coder talon "explain main.go"
```

The offline provider exercises the whole loop — tools, permissions, sessions —
without a network call. It is also what the integration tests use.

### Testing a change by hand

```bash
make build
cd /path/to/some/project
/path/to/talon/bin/talon --debug "run the tests and fix what fails"
```

Use `--cwd` instead of changing directory if you want to keep your shell where it
is, and start with `/permissions read-only` to see what the agent would do before
letting it act.

## Conventions

- `gofmt` everything; `make lint` enforces it.
- No dead code, no TODOs, no stubbed functions. A flag that exists must work.
- Comments explain why. Doc comments start with the identifier name.
- Errors go through `internal/errs` so users see a message and a hint.
- Tests must pass offline and without credentials.

## Release process

1. Update `CHANGELOG.md`.
2. Tag: `git tag -a v0.2.0 -m "…"` and push the tag.
3. Build with `-trimpath` and `-ldflags "-s -w"` for reproducible binaries.
4. Compute SHA-256 checksums and publish `checksums.txt` alongside the assets —
   `talon update` refuses to install an unverified binary.
5. GitHub Actions builds linux/darwin/windows binaries, the `.deb`, `.rpm` and
   `AppImage`, and attaches them to the release.
