# Security Policy

This document describes the controls Talon actually implements in this
repository, the limits of those controls, and how to report a problem. It is
kept honest on purpose: a security document that overstates what the code does
is worse than none.

## Reporting a vulnerability

Report privately rather than opening a public issue:

- GitHub Security Advisories: `https://github.com/talon-cli/talon/security/advisories/new`
- Email: `security@talon.dev`

Include the version, your OS and architecture, and a minimal reproduction. We
aim to acknowledge a report within 72 hours and to ship a fix or a mitigation
plan within 14 days.

## Supported versions

Security fixes land on the latest release line. Older lines receive fixes only
for issues rated high or critical.

## The model is not trusted

Talon treats the language model as an untrusted component. Everything the model
can influence — file content, command output, tool results, project metadata —
arrives as data, and the controls below live in Go code that the model cannot
read or rewrite:

- Content read from files is wrapped in an untrusted-content envelope that
  states it carries no authority (`internal/injection`).
- Every path is resolved inside the workspace and then passed through the
  sensitive-file gate (`internal/sensitive`, `internal/perm`).
- Every outbound request goes through one policy object; the provider client is
  the only HTTP client the program builds (`internal/netguard`).
- Every command runs through the shell runner, which applies the sandbox policy
  and records the decision (`internal/shell`, `internal/sandbox`).

## Controls in force by default

`[security]` in the configuration file; `talon security` prints what is in force.

| Control | Default | Where |
| --- | --- | --- |
| Sensitive files (`.env`, `~/.ssh`, cloud credentials, keychains…) | `block` | `internal/sensitive` |
| Credentials found in ordinary content | `mask` | `internal/sensitive` |
| Secret redaction in logs, errors, audit | on | `internal/secrets` |
| Network mode | `allowlist` | `internal/netguard` |
| Cloud metadata endpoints | always blocked | `internal/netguard` |
| Loopback / private IPs / plain HTTP | blocked (auto-enabled for local model providers) | `internal/netguard` |
| Kernel sandbox for commands | Landlock on Linux; reported honestly elsewhere | `internal/sandbox` |
| Command confirmation | on | `internal/perm` |
| Local audit log | on, JSONL, mode 0600, redacted | `internal/audit` |
| Telemetry | none exists in this build | — |
| Terms acceptance | required before use | `internal/legal` |

## What each layer actually does

### Sensitive data (`internal/sensitive`)

Paths that hold credentials are refused before the file is opened, so their
contents never enter a prompt. When a credential appears inside an ordinary
file, the value is replaced with a stable placeholder and the model is told
that a redaction happened. `security.sensitive_files` and
`security.content_findings` accept `block`, `mask`, `warn` and `allow`.

### Network (`internal/netguard`)

A single `RoundTripper` wraps the provider client. It refuses cloud metadata
endpoints unconditionally, enforces the allowlist, and blocks loopback,
private and link-local addresses unless the configuration or a local model
provider enables them. It also redacts request bodies before they reach a log.
DNS and UDP are not mediated: a host name is resolved by the resolver before the
address check, which is a real limitation documented in
`docs/security-review.md`.

### Prompt injection (`internal/injection`)

External content is wrapped with `<<<TALON-UNTRUSTED … authority=none>>>`, so a
file that says "ignore previous instructions" arrives as quoted data. Patterns
matching instruction override, exfiltration, remote execution and delimiter
escape are detected and reported; detection adds a notice, it does not silently
drop the file.

### Sandbox (`internal/sandbox`)

On Linux with Landlock, commands run in a helper process confined by the kernel:
filesystem access is limited to declared roots, `no_new_privs` is set, and
network access is limited to an explicit port list. Where no kernel sandbox
exists, `talon sandbox` says so; Talon does not pretend. Set
`security.sandbox_required = true` to refuse commands in that case.

### Audit (`internal/audit`)

Decisions are appended as JSONL with `0600` permissions: tool allow/deny,
approvals, network allows and blocks, redactions, injection warnings, sandbox
state, consent and data deletion. Credentials are redacted before writing.
Levels are `minimal`, `normal` and `verbose`.

### Privacy (`internal/privacy`) and legal (`internal/legal`)

`--privacy` disables sessions, memory, logs and the audit log. `talon privacy
data` lists every path Talon stores; `talon privacy clear <category>` deletes one
after confirmation. Terms and Privacy Notice versions are embedded in the binary
and acceptance is recorded in a local append-only ledger. Acceptance is never
inferred from continued use; only a material change asks again.

## Limits, stated plainly

- The provider receives the prompt and the tool results. Talon cannot control
  the provider's own logging or retention. Use a local provider if that must not
  happen.
- Landlock does not mediate DNS or UDP.
- `bwrap` and `firejail` are detected and reported but not used; Landlock is the
  only kernel isolation in use.
- The permission model protects the workspace, not the operating system. Talon
  itself runs with your user's privileges.
- Update verification is SHA-256 checksum based. There is no Ed25519 signature
  verification yet.
- Text in this repository claiming certification, audit or compliance status is
  not a claim Talon makes.

## Running with reduced risk

```bash
talon permissions read-only   # analysis and explanation only
talon --privacy "…"           # keep nothing on disk
talon --no-sandbox "…"        # only if you understand the trade-off
talon security                # what is in force
```

Use `--cwd` to point Talon at a scratch copy of the repository when experimenting
with unfamiliar code.

## Hardening recommendations

1. Run Talon as an unprivileged user, not root.
2. Keep `permissions.deny_commands` populated with commands you never want the
   agent to run in your environment.
3. Use a virtual machine or container for untrusted repositories.
4. Review `git diff` before committing anything the agent produced.
5. Prefer pinning a model you have evaluated for your codebase.
6. Use `--privacy` for work you do not want retained locally.
7. Check `talon sandbox` on each machine you use Talon on.