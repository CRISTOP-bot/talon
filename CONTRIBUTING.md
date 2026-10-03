# Contributing to Talon

Thanks for helping. This document covers how to build, test and land a change.

## Getting set up

```bash
git clone https://github.com/CRISTOP-bot/talon
cd talon
make build
make test
```

Requirements: Go 1.24+ and `git`. There are no third-party Go dependencies, and
adding one is a design decision that needs discussion — see
[docs/architecture.md](docs/architecture.md#dependencies).

## The workflow that will save you time

```bash
make lint          # gofmt -l + go vet
make test          # unit + integration, no network needed
make test-race     # race detector; run before pushing concurrency changes
```

Every phase of a change should end with: build, test, run the binary, fix what
breaks, then review your own diff for dead code and duplicated logic.

## Code conventions

- `gofmt` everything; `make lint` fails otherwise.
- One responsibility per file and per type. If a file needs a comment to
  explain what half of it does, split it.
- No dead code, no `TODO` placeholders, no stubbed-out functions that return
  plausible values. If a feature is not implemented, do not ship its flag.
- Errors are wrapped with `internal/errs` so the user sees a message and a hint
  instead of a stack trace. Never discard an error silently.
- Comments explain *why*, not *what*. Doc comments on exported identifiers start
  with the identifier name.
- Keep the agent loop provider-agnostic: no provider-specific types in
  `internal/agent`.

## Adding a tool

1. Write the definition in the matching file under `internal/tools`
   (`fs.go`, `shell.go`, `git.go`, `project.go`).
2. Give it a precise `Description` — it is sent to the model verbatim and is the
   only documentation it sees.
3. Give it a `Parameters` JSON schema and the correct `Risk`.
4. Set `PathArg`/`CommandArg` so the permission policy knows what to check.
5. Record file changes with `record(ctx, …)` so `/undo` works.
6. Add it to `All()` if it is built in.
7. Write tests in `internal/tools/tools_test.go` covering the happy path, the
   error paths and the schema validation.

## Adding a provider

Implement `llm.Provider` (`Name`, `ListModels`, `Stream`, `Complete`) in
`internal/llm`, register it in `registry.go`, and add its default base URL and
model catalogue entries. `Complete` is a helper on top of `Stream`, so there is
usually little to write. Test against `httptest` servers — never a real API.

## Tests

- Unit tests live next to the code as `*_test.go`.
- `internal/integration` drives the real REPL with a scripted provider; use it
  when a change spans more than two packages.
- Tests must pass offline. Do not require an API key, and do not sleep longer
  than a test needs — prefer synchronisation over timing.
- Prefer asserting on observable behaviour (files on disk, output text, git
  state) over internal state.

## Commits and pull requests

- Conventional-ish subjects: `feat:`, `fix:`, `docs:`, `test:`, `refactor:`.
- One logical change per commit; unrelated reformatting makes review harder.
- Describe what a reviewer should check: behaviour change, risky paths,
  anything you deliberately did not do.
- Update `CHANGELOG.md` under `[Unreleased]` and the docs when behaviour changes.

## Reporting bugs

Open an issue with the version (`talon version`), your OS, the provider and
model, the project type, and a transcript. `--debug` writes a redacted log under
`~/.local/state/talon/logs/` that is usually enough to diagnose the problem.

## Security issues

Do not open a public issue. Follow [SECURITY.md](SECURITY.md).
