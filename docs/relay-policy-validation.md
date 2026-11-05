# Relay policy validation

Validation performed on 2026-11-05 for `feat/relay-groups-pair-policies`.
The implementation spans the NetBird, Dashboard and documentation repositories.
U1–U8 were committed separately before deferred test execution began.

## Host-safe verification

- All feature packages pass, including the complete client engine, peer, local
  status, relay registration, relay administration, HTTP handlers, SQL store,
  network-map goldens, shared domain and protocol packages.
- Full peer and relay registration suites pass with race detection. Focused
  engine policy revocation, stream reporter cancellation, report batching and
  signaling cancellation checks pass with race detection.
- SQLite and real PostgreSQL upgrade tests preserve existing account, peer,
  user, group, ACL and network state. GORM and pgx snapshots agree, including
  nullable legacy network serials and absent relay configuration.
- Changed-code Go lint passes with zero issues, including tagged E2E harness
  and acceptance code. Service builds and Windows/macOS client builds pass.
- Dashboard: 87 unit files and 1,008 tests pass. Scoped ESLint and full TypeScript
  checking pass. Production build passes after the final UI fixes. All seven relay Playwright
  cases pass against real OIDC, a disposable current backend and three connected
  Docker clients (44.9 seconds, no retries).
- Documentation production build and all 311 MDX heading checks pass.

The full host-safe suite was executed. Remaining failures in Agent Network
private-cluster fixtures, Go 1.27 JSON conversion and a Redis connection-failure
fixture reproduce on pristine base `3c996c91dda48e722c4b735f8d1028beda02cb79`.
The Redis fixture assumes nothing listens on host port 6379; this host has Redis.
Desktop verification requires missing frontend build output and GTK/WebKit/libsoup
libraries. Full repository lint stops at the desktop embedding prerequisite;
Dashboard repository-wide lint reports existing violations outside this change.
FreeBSD cross-compilation with CGO disabled fails identically on the base revision
in the existing wgctrl dependency. These checks are not represented as passing.

## Visual inspection

The full Dashboard comparison produced 329 unchanged, 461 changed and 35 new
screenshots. Of the changed stories, 442 change only within the sidebar after the
Relays navigation item was added; 18 peer stories also gain the Relays tab. The
remaining story has a three-pixel-high chevron rasterization difference, confirmed
by a focused repeat capture. Catalog, denied, stale, retry and status states were
inspected. No baseline or threshold was altered.

## Performance observations

Illustrative loaded-host measurements, not latency guarantees:

| Operation | Measurement |
| --- | --- |
| Network-map policy computation, 3 peers | 23.1 microseconds/op; 3,419 bytes/op |
| Network-map policy computation, 100 peers | 1.03 milliseconds/op; 142,577 bytes/op |
| Network-map policy computation, 1,000 peers | 12.0 milliseconds/op; 1,578,323 bytes/op |
| 128-target report validation, shared batch indexes | 2.08 milliseconds; 714 KB; 4,351 allocations |
| Prior repeated report indexes, same benchmark | 112 milliseconds; 66.9 MB; 325,978 allocations |

## Review

Correctness, testing, standards, maintainability, security, adversarial, API,
reliability, performance, migrations, TypeScript, async UI, agent parity and
repository learnings reviews ran. Confirmed findings were fixed and rechecked.
The migration review also added the executed pre-feature upgrade regression.

## Docker runtime acceptance

All nine top-level tests passed in two isolated invocations. No test failed or
skipped in the final runs. The six-combination matrix completed in 862.784 seconds;
the sequential eight specialized scenarios completed in 622.214 seconds. The
older-client scenario ran `netbirdio/netbird:0.82.0`.

| Network-map version | Kernel | Userspace TUN | Netstack |
| --- | --- | --- | --- |
| Legacy (0) | Passed, 157.34 s | Passed, 141.30 s | Passed, 137.22 s |
| Component (1) | Passed, 140.28 s | Passed, 139.74 s | Passed, 138.43 s |

Every combination checks forced native relay A, an unrelated direct connection,
concurrent A/B pairs, live exclusion, outage fallback, targeted recovery, and
live clearing of pair and home policy without restarting clients. It checks
actual unique HTTP bytes, matching fresh endpoint observations, and relay
forwarding counters.

The specialized cohort passed home-profile independence, sticky/manual failover
and no C escape, offline detection, editable-membership rejection, automatic
membership conflict, Signal interruption, independent Management authorities,
PostgreSQL replica catalog replacement/retirement, old-client enforcement, and
isolated customer SQL/HTTP traffic at overlapping addresses. Negative delivery
checks verify destination service health and require sustained denial after
bounded asynchronous update convergence.

Representative legacy/kernel measurements from the matrix:

- Initial forced A: revision 1, catalog 1, attempt 1, convergence 8.537 s;
  digest `7dc7f9fb9ba5577e325e5767d179c34995decfcc34149f72f4066551373962b6`.
- A outage to B: revision 4, catalog 1, attempt 2, convergence 26.780 s;
  digest `19fa060041d232504deafbc85c348c29f3dd2a9a45c7fdb9d5f2a12a43d3c674`.
- Targeted recovery to A: revision 4, catalog 1, attempt 1, convergence 14.597 s;
  digest `410088f03df1e3b5217a2e83b655893a65c38dafc23c56b2d2f51eb39eb11f61`.

These are observed convergence times including polling/reporting, not a failover
SLA or a measurement of every packet's interruption window.

Frozen source-built image IDs used for the final runtime acceptance:

- `local/netbird-client:relay-policy-acceptance`: `sha256:5059cbeb3cee071b0486190d50bbb08d9e4835bf9e502c4d260d5975f4085658`.
- `local/netbird-combined:relay-policy-acceptance`: `sha256:9441aa4f8c09d618250e2c0a5c57da7c3afb7f864f5e320d67df6c301396f170`.
- `local/netbird-relay:relay-policy-acceptance`: `sha256:b1e72f129ddca7a301a5502699eb3152c2a25b09e9b7c8cdb0c7d245290cfce3`.

The controlled relays use plain `rel://` inside disposable Docker networks. This
run does not establish external TLS/certificate behavior, public NAT traversal,
mobile-platform execution, or routing to a physical customer LAN. Shared HMAC
credentials enforce cooperative client selection; this is not relay-side
per-account authorization or independent geographic-compliance proof.

## Reproduction and retained evidence

Follow [the acceptance README](../e2e/relaypolicy/README.md) and
[the operator runbook](relay-policy-poc.md). The two final invocations split the
matrix and specialized scenarios while preserving all test coverage:

```sh
NB_E2E_OLD_CLIENT_IMAGE=netbirdio/netbird:0.82.0 \
  go test -tags 'e2e devcert' -count=1 -timeout=45m -v \
  -run '^TestForcedPairLeavesOtherDirectAndSupportsConcurrentPins$' ./e2e/relaypolicy
NB_E2E_OLD_CLIENT_IMAGE=netbirdio/netbird:0.82.0 \
  go test -tags 'e2e devcert' -count=1 -timeout=45m -v \
  -skip '^TestForcedPairLeavesOtherDirectAndSupportsConcurrentPins$' ./e2e/relaypolicy
```

The final runs additionally set the documented image overrides to the frozen
local images above. All clients, relays, databases, Signal servers and Management
instances were disposable; harness cleanup ran on successes and failures. No
host NetBird client, deployment, push or pull request was performed.

Local raw evidence from this execution is retained outside the repository:

- `/tmp/netbird-relay-acceptance-results.json`
- `/tmp/netbird-relay-acceptance-matrix.log`
- `/tmp/netbird-relay-acceptance-specialized-final.log`
- `/tmp/netbird-dashboard-playwright/README.md`, `run.log`, `results.json`
- `/tmp/netbird-test-unit-final.log`, `/tmp/netbird-relay-client-final.log`
- `/tmp/netbird-relay-baseline-failures.log`
- `/tmp/compound-engineering/ce-code-review/relay-groups-20261104/`

Raw temporary files are local execution evidence, not a durable external archive.
This document preserves the results, boundaries and immutable image identities.
