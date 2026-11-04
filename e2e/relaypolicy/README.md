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
that the daemon actually uses the requested interface mode. For a narrower run:

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
| 1: forced pair and unrelated direct path; 2: concurrent A/B pins | `TestForcedPairLeavesOtherDirectAndSupportsConcurrentPins` |
| 3: different home profiles retain foreign connectivity | `TestHomeProfilesDoNotRestrictForeignPairs` |
| 4: outage fallback and no C escape; 5: sticky/manual switch; 6: live exclusion; 12: targeted retry | `TestFailoverStickyManualExclusionAndTargetedRetry` |
| 7: offline endpoint and runtime membership conflict | `TestOfflineAndRuntimeMembershipConflictCannotLookApplied` |
| 7: old-client compatibility and unsupported membership | `TestOlderClientCompatibilityAndUnsupportedMembership` |
| 7: catalog retirement; 11: shared PostgreSQL replicas, replacement and CAS | `TestReplicasObserveAtomicCatalogReplacementAndRetirement` |
| 8: independent authorities sharing physical relays | `TestIndependentAuthoritiesSharePhysicalRelaysWithoutConfiguration` |
| 9: overlapping customer database/HTTP addresses and ACL isolation | `TestOverlappingCustomerDatabaseAndHTTPRoutesStayIsolated` |
| 10: signal interruption and stale observation rejection | `TestSignalResetCannotApplyStalePath` |

Verbose output records policy revision, digest, catalog generation, native
attempt, and convergence duration. Negative delivery checks first verify that
the destination service is alive. Relay counters establish actual forwarding;
endpoint reports establish agreement with desired instructions. These are
runtime acceptance checks, not independent cryptographic proof that a relay
enforces a pair policy.

The suite was authored with test execution deferred until all implementation
units are committed. Compilation-only verification does not execute Docker
acceptance scenarios and must not be reported as a passing acceptance run.
