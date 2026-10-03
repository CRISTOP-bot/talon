# Permissions and approvals

This document explains how Talon decides whether the agent may read, write or
run something, and how that interacts with the security controls in
`SECURITY.md`.

## Levels

| Level | Reads | Writes inside the workspace | Commands | Destructive tools |
| --- | --- | --- | --- | --- |
| `read-only` | allowed | denied | denied | denied |
| `safe` | allowed | allowed | ask | ask |
| `confirm` (default) | allowed | ask | ask | ask |
| `full-access` | allowed | allowed | allowed | ask |

Set the level with `talon permissions <level>` or
`permissions.level` in the configuration file.

Two rules apply at every level, including `full-access`:

- An explicit deny wins. `permissions.deny_paths`, `permissions.deny_commands`
  and `permissions.deny_tools` are checked before the level is consulted.
- Destructive tools always ask. `delete_file` is classified as dangerous and is
  confirmed even in `full-access`.

The baseline deny list for commands is deliberately small — it blocks the
irreversible classics (`rm -rf /`, `mkfs`, `dd if=`, fork bombs, `shutdown`,
`reboot`, `chown -R /`, force-push, and piping a download into a shell). The real
protection is the level: at `confirm` and `safe`, a command that is not on
`permissions.allow_commands` needs a human.

## Approvals

When a decision is `ask`, the REPL shows the tool, the risk class, the exact
command or path, and any dangerous patterns detected in it. Answers:

- `y` — allow once
- `a` — allow this tool for the rest of the session
- `n` — refuse

A refusal is a normal outcome, not an error. `--yes` answers `y` automatically,
but the permission level still applies, and `--privacy` disables it entirely so a
private run always shows what it is about to do.

## Paths

Every filesystem tool resolves the path against the workspace and re-checks
containment afterwards, so `../../etc/passwd` and symlinks that leave the tree
are refused. Escape is possible only through `permissions.allow_paths`.

On top of containment, `internal/sensitive` refuses credential files by name
(`.env`, `id_rsa`, `*.pem`, `.npmrc`, `.git-credentials`, cloud service-account
keys, keychains) unless `security.sensitive_files` says otherwise. This is a
separate check from permissions: it is about the data, not about the location.

## Commands

Commands run through one runner that applies, in order:

1. the permission decision (allow / ask / deny),
2. the command deny and allow lists,
3. the sandbox policy, when one is available,
4. environment filtering, so credentials are not inherited by the child,
5. an audit record of the decision and the outcome.

`talon sandbox` reports what the kernel can enforce on the current machine.
Where nothing is available, that is stated rather than glossed over; set
`security.sandbox_required = true` to refuse commands in that situation.

## Network permissions

Network access is not per-tool: it is one policy for the whole process
(`internal/netguard`). See `SECURITY.md` for the allowlist, the metadata block
and the loopback/private-IP rules. There is no tool that fetches an arbitrary
URL on the model's behalf.

## What is recorded

Approvals, refusals, tool outcomes, network decisions, redactions and sandbox
state go to the local audit log (`talon audit`). Content of conversations is not
written to the audit log, and credentials are redacted before anything is
written at all.

## Reviewing a run

```bash
talon permissions            # current level
talon security               # every control in force
talon audit -n 50            # what was allowed, refused and why
talon diff                   # what changed on disk
```