# Security review

An honest review of the current state of Talon's security work: what has been
verified, what is known to be weak, and what is deliberately not claimed.

**This is not an audit and not a certification.** No third party has reviewed
this code. Nothing here should be read as a compliance claim.

## Verified in this environment

| Check | Result |
| --- | --- |
| Landlock ABI available | ABI 10 detected; commands run confined |
| Read outside allowed roots | blocked by the kernel |
| Write outside allowed roots | blocked by the kernel |
| Write inside the workspace | allowed |
| Cloud metadata endpoint through the provider client | refused |
| Loopback / private IP with default configuration | refused |
| Credential in an ordinary file | masked before reaching the model |
| `.env` read attempt | refused, and the denial is audited |
| Credential written to the audit log | redacted before writing |
| Privacy mode | no file written under the data directory |
| Consent | only an explicit act records acceptance; a material change re-asks |

Reproduce with `make lint && go test ./... && go test -race ./...` and the
commands in `docs/verification.md`.

## Open risks, ranked

### 1. No kernel sandbox on macOS and Windows

Commands there run with the user's full privileges; only Talon's own path
containment, environment filtering and command policy apply. Landlock covers
Linux only.

*Mitigation in place:* `talon sandbox` reports the gap, and
`security.sandbox_required = true` turns it into a refusal. Running Talon inside
a VM or container is the real mitigation.

### 2. DNS rebinding and UDP are not mediated

The network policy resolves the host, checks the address, then connects to the
host name. A name that resolves to a public address during the check and to
`127.0.0.1` at connect time is not caught. Landlock cannot restrict DNS or UDP,
so a sandboxed command may resolve and send UDP freely.

*Mitigation in place:* loopback and private ranges are refused by policy, so only
a rebinding attacker with a cooperating resolver can exploit this. A fix would
pin the connection to the validated address.

### 3. Plugin and MCP code runs unsandboxed

Plugins are separate executables and MCP servers are separate programs. Talon
installs and starts them on the user's instruction, with the user's privileges.

*Mitigation in place:* installation is explicit, `talon plugins test` runs a
plugin before it is trusted, and the manifest decides what is executed. There is
no confinement of the plugin itself.

### 4. Updates are checksum-verified, not signed

`talon update` verifies SHA-256 checksums published alongside the release. If
the release repository itself were compromised, a matching checksum would not
help.

*Mitigation in place:* the download is confined to the configured repository and
installed atomically, with the previous binary kept. Ed25519 signature
verification is the intended fix and is not implemented.

### 5. The model can still be talked into trying

Wrapping content as untrusted data and masking credentials raise the cost of a
successful injection; they do not eliminate the possibility that a sufficiently
persistent prompt influences the model's next step. The controls that matter
after that — permissions, sandbox, network policy — are not under the model's
control.

### 6. Detection is heuristic

Injection scanning and credential detection use patterns. They will miss novel
phrasings and can produce false positives, which is why matches produce a notice
to the model and an audit record instead of silently dropping content.

### 7. No privacy mode for the provider side

`--privacy` controls local storage only. The prompt still goes to the configured
provider. Only a local provider (`ollama`, `llamacpp`) keeps inference on the
machine.

## Legal text

`TERMS.md` and `PRIVACY.md` mirror the documents embedded in the binary and a
test fails if they drift. The texts state plainly that they have not been
reviewed by a lawyer; anyone who needs a legally sufficient version for an
organisation should have one drafted.

## What we deliberately do not claim

- No SOC 2, ISO 27001 or similar certification exists.
- No penetration test has been performed by an external party.
- No formal threat-model validation or compliance attestation exists.
- The absence of telemetry is a property of this codebase, not a policy promise
  about future versions.