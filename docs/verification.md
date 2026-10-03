# Verification

Two kinds of checks: automated tests that exercise the gates, and manual steps
that exercise the assembled program. Every step states what it expects; if one
does not produce that output, treat it as a defect rather than a documentation
mismatch.

## 1. Build and test

```bash
make lint
go test ./...
go test -race ./...
make cross
```

## 2. Automated coverage of each control

The gates are reached through tool calls, so the tests call the tools directly.

```bash
go test ./internal/sensitive/  ./internal/secrets/   # data classification and masking
go test ./internal/netguard/                        # allowlist, metadata, loopback, transport
go test ./internal/injection/                       # provenance wrapping and pattern detection
go test ./internal/sandbox/                         # Landlock confinement, honest fallback
go test ./internal/audit/                           # permissions, levels, redaction on write
go test ./internal/privacy/                         # inventory, plans, deletion
go test ./internal/legal/                           # consent, version chain, document mirroring
go test ./internal/secure/                          # the assembled posture and its defaults
go test ./internal/tools/                           # gates wired into real tool calls
```

Specific properties worth naming:

| Property | Test |
| --- | --- |
| `.env` cannot be read into a prompt | `TestReadFileRefusesSensitivePath` |
| A key in an ordinary file is masked | `TestReadFileMasksSecretsInContent` |
| Search results do not leak keys | `TestSearchTextDoesNotLeakSecrets` |
| File content arrives as `authority=none` data | `TestReadFileWrapsProvenance` |
| Oversized files are refused | `TestReadFileRefusesOversizedFile` |
| Denials are audited | `TestAuditRecordsDenial` |
| Credentials never reach the audit log | `TestAuditNeverStoresCredentials` |
| Metadata endpoints are always blocked | `TestBlockedMetadataEndpoints` |
| Loopback and private IPs are blocked by default | `TestPrivateAndLoopbackBlockedByDefault` |
| HTTPS is required | `TestHTTPSRequiredByDefault` |
| The policy applies to the real client | `TestTransportEnforcesPolicy` |
| Credential headers cannot be smuggled | `TestTransportRefusesSmuggledCredentialHeaders` |
| Landlock blocks reads outside the roots | `TestKernelIsolationBlocksReadingOutsideRoots` |
| Landlock confines writes | `TestKernelIsolationConfinesWrites` |
| A missing sandbox is reported, not faked | `TestDetectIsHonest`, `TestCommandFallsBackWithoutKernelSupport` |
| Child environments drop credentials | `TestFilterEnvDropsCredentials` |
| Defaults are the restrictive ones | `TestDefaultsAreSafe` |
| Telemetry cannot be enabled | `TestTelemetryCannotBeEnabled` |
| Privacy mode writes nothing | `TestPrivacyModeWritesNothing` |
| Deletion touches only planned paths | `TestApplyDeletesPlannedItemsOnly` |
| Consent is not implicit | `internal/legal` ledger tests |

## 3. Manual: the posture that actually runs

```bash
talon security
talon sandbox
talon privacy data
```

Expect: `sensitive files block`, `credentials in content mask`, `network mode
allowlist`, `http permitted disabled`, `cloud metadata hosts always blocked`,
`telemetry disabled (this build has no telemetry)`, and a sandbox line naming
Landlock or stating plainly that there is none.

## 4. Manual: privacy mode keeps nothing on disk

```bash
rm -rf /tmp/private-home
HOME=/tmp/private-home talon --privacy --accept-terms "say hi"
find /tmp/private-home -type f | wc -l
```

Expect the acceptance to be refused (`--accept-terms` needs disk), then, with the
flag dropped:

```bash
rm -rf /tmp/private-home
HOME=/tmp/private-home talon --privacy "say hi"
find /tmp/private-home -type f | wc -l   # 0
```

## 5. Manual: consent is explicit

```bash
talon terms            # prints the Terms, records nothing
talon terms history    # still empty
talon terms accept     # records acceptance
talon terms history    # version, timestamp and source
```

## 6. Manual: data can be inspected and removed

```bash
talon data
talon audit            # decisions so far
talon data clear audit # asks first; answering n cancels
talon data             # the audit log is gone
talon history          # input history, if any
```

## 7. Manual: documented text matches served text

```bash
go test ./internal/legal/ -run Documents -v
```

Expect: the repository `TERMS.md` and `PRIVACY.md` contain the embedded text
verbatim.

## Note on the mock provider

`AI_PROVIDER=mock` echoes the prompt and never calls a tool, so it cannot be used
to observe gate behaviour end to end. Gate behaviour is covered by the tool tests
in section 2; the mock is useful for checking startup, privacy mode, consent and
the audit trail.