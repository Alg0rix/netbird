# Relay policy Docker acceptance

This suite starts source-built Management, Signal, relay, and client containers.
It verifies delivery of unique HTTP response bodies, matching observations from
both endpoints, and physical relay forwarded-byte counters. PostgreSQL sidecars
exercise catalog publication and overlapping customer service addresses.

Run from the repository root after building and committing the implementation:

```sh
go test -tags 'e2e devcert' -timeout 45m -v ./e2e/relaypolicy
```

Docker must support container networking, `NET_ADMIN`, `/dev/net/tun`, and the
host's kernel overlay interface module for the kernel cases. Client networking
changes happen inside disposable Docker namespaces. Do not start a host NetBird
client for this suite. The tests clean up clients, services, networks, and
account configuration on failures as well as success.

By default, the first scenario runs kernel, userspace, and netstack clients with
both legacy (`0`) and component (`1`) Management network-map forms. It checks
that the daemon actually uses the requested interface mode. Each of these six
fixtures also exercises live exclusion, relay outage fallback, targeted recovery,
and CAS clearing of pair and home policies without restarting clients. Clearing
requires both local managed scopes to disappear and direct HTTP delivery to
resume. The remaining scenarios run userspace clients with the default negotiated
network-map form; PostgreSQL replication, old binaries, and routed customer
services are not repeated across the six combinations. For a narrower run:

```sh
NB_E2E_RELAY_MODES=userspace,netstack \
  go test -tags 'e2e devcert' -timeout 45m -v ./e2e/relaypolicy
```

The harness builds local images unless overridden with
`NB_E2E_CLIENT_IMAGE`, `NB_E2E_COMBINED_IMAGE`, `NB_E2E_RELAY_IMAGE`, or
`NB_E2E_SIGNAL_IMAGE`. Overrides must contain the corresponding current binaries
and preserve the harness image entrypoint and tooling. `NB_E2E_BUILDX_CACHE`
selects a build cache directory. Docker also pulls `postgres:17-alpine` and the
harness HTTP probe image.

Set `NB_E2E_OLD_CLIENT_IMAGE=netbirdio/netbird:0.82.0` to exercise the older-client
case. An alternate image must lack relay-policy capability 4 and provide a
compatible NetBird entrypoint, shell, and busybox. Without this variable that
case reports an explicit skip. It tests an older binary entering an existing
policy scope; it does not model replacing a running binary in place while
preserving the same identity.

| Plan scenarios | Acceptance test |
| --- | --- |
| 1: forced pair and unrelated direct path; 2: concurrent A/B pins; live exclusion/fallback/retry and pair/home clearing in all six combinations | `TestForcedPairLeavesOtherDirectAndSupportsConcurrentPins` |
| 3: different home profiles retain foreign connectivity | `TestHomeProfilesDoNotRestrictForeignPairs` |
| 4: outage fallback and no C escape; 5: sticky/manual switch; 6: live exclusion; 12: targeted retry | `TestFailoverStickyManualExclusionAndTargetedRetry` |
| 7: offline endpoint, atomic editable-membership rejection, and automatic enrollment conflict | `TestOfflineAndRuntimeMembershipConflictCannotLookApplied` |
| 7: old-client compatibility, rejected editable activation, and unsupported automatic enrollment | `TestOlderClientCompatibilityAndUnsupportedMembership` |
| 7: catalog retirement; 11: shared PostgreSQL replicas, replacement and CAS | `TestReplicasObserveAtomicCatalogReplacementAndRetirement` |
| 8: independent authorities sharing physical relays | `TestIndependentAuthoritiesSharePhysicalRelaysWithoutConfiguration` |
| 9: overlapping customer database/HTTP addresses and ACL isolation | `TestOverlappingCustomerDatabaseAndHTTPRoutesStayIsolated` |
| 10: signal interruption and stale observation rejection | `TestSignalResetCannotApplyStalePath` |

Editable group changes reject incompatible activation atomically. The membership
and older-client cases then enroll real clients using setup-key automatic groups
to exercise runtime conflict and capability enforcement through a separate path.
Catalog retirement waits for actual revocation after asynchronous map delivery,
then requires sustained denied delivery from a healthy destination service.

Verbose output records policy revision, digest, catalog generation, native
attempt, and convergence duration. Negative delivery checks first verify that
the destination service is alive. Relay counters establish actual forwarding;
endpoint reports establish agreement with desired instructions. These are
runtime acceptance checks, not independent cryptographic proof that a relay
enforces a pair policy.

Compilation-only verification does not execute Docker acceptance scenarios and
must not be reported as a passing acceptance run. Capture runtime results from
the verbose command above, including explicit old-client skips and mode limits.
