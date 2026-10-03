# Security model

This document describes what Talon defends, from what, and where the limits
are. It is the companion to `SECURITY.md` (what is in force) and
`docs/security-review.md` (what is still open).

## 1. Assets worth protecting

| Asset | Why it matters |
| --- | --- |
| Credentials in the environment and in files | A leaked key is a leaked account |
| Proprietary source code | Sending it to a hosted provider may breach an agreement |
| Host integrity | A command the model can run is a command the user effectively ran |
| Consent record | Acceptance must not be fabricated or inferred |
| The user's attention | Silent autonomy is the failure mode people care about |

## 2. Adversaries

1. **A hostile repository.** Code, comments, READMEs and build output can
   contain text crafted to steer the agent.
2. **A compromised dependency or plugin.** It executes with the user's
   privileges; Talon only limits what it hands over.
3. **A malicious or misconfigured provider.** It sees prompts and can try to
   induce tool use.
4. **A confused user.** Unattended mode plus full access is a foot-gun.

## 3. Trust boundaries

```
   user keystrokes
        │
        ▼
   ┌──────────────┐   untrusted data envelope    ┌────────────────┐
   │   Talon      │ ───────────────────────────▶ │ model provider │
   │ (trusted)    │ ◀─────────────────────────── │ (untrusted)    │
   └──────┬───────┘   provider response           └────────────────┘
          │ gates: sensitive · netguard · sandbox · perm · audit
          ▼
   shell / filesystem / network
```

Everything inside the box labelled *Talon* is Go code the model cannot read,
rewrite or influence. The model only supplies data and tool arguments; every
boundary crossing above is checked by code on this side.

## 4. Controls by threat

### Hostile repository content

- External content is wrapped as data with `authority=none`
  (`internal/injection`), so instructions inside a file are quoted, not obeyed.
- Injection patterns (instruction override, exfiltration, remote execution,
  policy tampering, delimiter escape) produce an explicit notice to the model
  and an audit record.
- Credential-shaped strings are masked before content is sent
  (`internal/sensitive`).
- Files that are credentials by name are refused outright.

### Exfiltration through the network

- One `RoundTripper` mediates all provider traffic (`internal/netguard`).
- Cloud metadata endpoints are refused unconditionally.
- Loopback, private and link-local addresses are refused unless explicitly
  enabled or required by a local model provider.
- Plain HTTP is refused unless enabled; local providers turn it on for their own
  server only.
- Request bodies are redacted before they can reach a log.

### Arbitrary execution

- The model has no shell of its own: commands go through `run_command`,
  `run_tests` and `build_project`, each gated by `internal/perm` and
  `permissions.deny_commands`.
- On Linux with Landlock, commands run in a kernel-confined helper process with
  `no_new_privs` set and filesystem access limited to declared roots.
- Child processes receive an environment with credentials removed
  (`internal/secrets.FilterEnv`).
- Path traversal is resolved and re-checked against the workspace.

### Silent autonomy

- Destructive tools always ask, even at `full-access`.
- `--yes` is honoured but bounded by the permission level.
- `--privacy` disables unattended approval.
- Every decision is appended to a local audit log with `0600` permissions.

### Consent fabrication

- Acceptance is written by an explicit act only (`talon terms accept`,
  `--accept-terms`); reading the Terms never records anything.
- Only a version marked as a material change asks again.
- The ledger stores the document hash, so a changed document cannot be covered
  by an old acceptance.

## 5. Out of scope

- The security of the operating system, the shell, or the user's account.
- The provider's retention and training practices.
- Malicious plugins and MCP servers: they are separate programs that Talon
  starts on purpose. Review them.
- Anything that requires the model to cooperate. The controls assume the model
  may try anything; none of them depend on the model behaving.

## 6. Design rules for future changes

1. A new tool must go through the existing gates. A tool that touches the
   filesystem, the network or a subprocess without a gate is a defect.
2. New configuration keys default to the restrictive value and are validated.
3. Errors returned to the model must not contain secrets.
4. A control that cannot be enforced must be reported, not assumed — see
   `talon security` and `talon sandbox`.
5. Tests must include the adversarial case: the malicious input, not only the
   happy path.