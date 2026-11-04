# Relay policy acceptance runbook

This runbook targets the `feat/relay-groups-pair-policies` fork. No released
NetBird version is claimed to contain these controls. Record the exact Management,
Signal, Relay, agent and Dashboard commits or image digests for every run. The
commands below are procedures to execute after implementation commits, not a
record of successful tests or measured failover times.

## Prepare an isolated lab

Use the Docker E2E harness in `e2e/relaypolicy/`, with Docker, BuildKit and the Go
version required by `go.mod`. The harness owns its containers and network and must
clean them up on success, failure and interruption. Run privileged networking only
inside that harness. Do not run `netbird up`, change firewall rules, or stop relays
on the current host, a shared server or a Kubernetes deployment.

Provision independently controllable relays A and B, plus unrelated relay C. C is
an escape detector: a policy allowing only A/B must never use C. A one-relay lab
cannot establish failover acceptance. Use disposable clients with an existing ACL
that permits the intended pairs, a separate unauthorized pair, and application
responders returning distinct markers. Relay policy must not create access.

For customer-isolation tests, use two accounts or Management deployments, an HTTP
responder for one customer and a database responder for the other, with overlapping
private destination addresses. Markers must distinguish which responder answered.
Sidecar/loopback responders establish application isolation only; they do not
validate routing to a physical customer LAN.

Record this manifest before starting:

- Source commits/image digests, client version and advertised relay-policy
  capability, networking mode (kernel, userspace or netstack), operating system.
- Both network-map delivery forms, current and older client images, SQL backend,
  Management replica count and account IDs represented by non-sensitive lab IDs.
- Stable approved relay IDs, client endpoints and handshake instance endpoints;
  relay container IDs and client container IDs needed for fault injection.
- ACL policy, peer-group memberships, expected positive and negative flows.
- UTC clocks and event timestamps; no PATs, setup keys, relay secrets or private
  keys in the evidence directory.

An older client image is a compatibility fixture. It must not replace every client
in a restrictive positive-path test. Record which endpoint is old in mixed runs.

## Understand scope before configuration

A **home profile** controls a peer's relay registrations. It does not pin all its
pairs to that region or restrict their transports. A **pair policy** controls the
actual transport for an unordered, ACL-authorized pair. It can require native
relaying even when a direct path is possible. One peer can simultaneously use A
for one pair and B for another.

Peer groups select peers; relay groups select approved relays. Matching pair rules
intersect their allowed sets and accumulate exclusions and force-relay constraints.
An explicit pair rule cannot undo an inherited exclusion. Read effective
contributors and edit the owning broader rule, previewing every affected pair.
Home and pair rules resolve independently.

A single permitted relay is a pin: its outage has no relay fallback. Ordered tiers
`[A], [B]` mean try A, then B. Entries within a tier can race. After automatic
failover, a working B path stays on B when A recovers. Change persistent priority
to promote B, or use **Retry preferred** to retry the existing order after A
recovers. Retry targets one home scope or one unordered pair and may interrupt
traffic; permitted fallback remains available. It is not a remote command/job.

## Publish the operator catalog

Account users can only reference operator-approved, active, non-legacy relay IDs.
Addresses discovered from a peer offer are not approval. Existing advertisements
bootstrap as legacy entries and preserve unconfigured behavior; they cannot be
selected by managed account rules.

For standalone Management, preserve the rest of `management.json` and add explicit
entries to its existing `Relay` object. This fragment omits existing credential
settings intentionally; do not replace the complete configuration with it:

```json
{
  "Relay": {
    "Catalog": [
      {"ID": "relay-a", "Endpoint": "rels://a.example.com:443", "InstanceEndpoint": "rels://a.example.com:443", "Aliases": [], "Active": true, "Legacy": false},
      {"ID": "relay-b", "Endpoint": "rels://b.example.com:443", "InstanceEndpoint": "rels://b.example.com:443", "Aliases": [], "Active": true, "Legacy": false},
      {"ID": "relay-c", "Endpoint": "rels://c.example.com:443", "InstanceEndpoint": "rels://c.example.com:443", "Aliases": [], "Active": true, "Legacy": false}
    ]
  }
}
```

For the combined server, the branch accepts the same publication input under
`server.relays.catalog` in `config.yaml`. Merge this into the existing relays
section, preserving addresses, credentials and all other server settings:

```yaml
server:
  relays:
    catalog:
      - id: relay-a
        endpoint: rels://a.example.com:443
        instanceEndpoint: rels://a.example.com:443
        aliases: []
        active: true
      - id: relay-b
        endpoint: rels://b.example.com:443
        instanceEndpoint: rels://b.example.com:443
        aliases: []
        active: true
```

There is no combined-server `admin relay-catalog` command in this branch. For
operator publication, use the standalone Management admin binary and a protected
Management-format JSON configuration pointing at the combined deployment's **same
database**, data directory and encryption key. Do not copy YAML into the JSON
reader or accidentally open a new empty database. The harness's
`Combined.PublishRelayCatalog` helper is a test helper that writes through the
real SQL repository, not an account-facing HTTP mutation endpoint.

Use the exact `NB_EXPOSED_ADDRESS` returned by each relay handshake as its approved
`InstanceEndpoint`. A load balancer must not make distinct relay instances appear
interchangeable. Add only aliases controlled by the operator; never add a peer's
untrusted offered address to make negotiation pass.

Run the branch's standalone Management binary against the intended lab database
and its matching configuration/encryption key. Preview is read-only. Publication
requires the generation that was actually reviewed:

```bash
netbird-mgmt admin relay-catalog preview --config ./management.json > catalog-preview.json
jq '{Generation, NextGeneration, AccountIDs, Retired, Replaced}' catalog-preview.json
NB_POC_GENERATION=$(jq -er '.Generation' catalog-preview.json)
netbird-mgmt admin relay-catalog publish --config ./management.json \
  --expected-generation "$NB_POC_GENERATION" > catalog-publication.json
```

The active immutable generation is shared in SQL; changing a replica's local
configuration or restarting it does not republish an existing catalog. On a
conflict, read/preview again. Endpoint replacement or retirement must go through
the same impact review, including affected accounts and reconnects. Preserve IDs
only when their approved identity remains meaningful.

## Configure and preview an A/B pair

The Dashboard shows **Relays** only when Management reports
`GET /api/relay-policies/configuration` with `supported: true` and the caller has
`relays.read`. Older Management returning 404 hides these controls. There is no
version-number shortcut for support. Owners, admins and network admins can manage
configuration; auditors can inspect it; ordinary/restricted users cannot administer
it. The server enforces the same permissions for direct REST calls. Retry requires
update permission. Operator catalog publication is a separate server operation.

Use lab PATs supplied outside the evidence directory. These examples assume
`NB_POC_API` includes `/api`, and peer IDs identify already authorized lab peers:

```bash
export NB_POC_API=https://management.example.com/api
read -rs -p 'Lab PAT: ' NB_POC_PAT
export NB_POC_PEER_A=lab-peer-a NB_POC_PEER_B=lab-peer-b
curl --fail-with-body -sS -H "Authorization: Token $NB_POC_PAT" \
  "$NB_POC_API/relay-policies/configuration" > configuration-before.json
jq --arg a "$NB_POC_PEER_A" --arg b "$NB_POC_PEER_B" \
  '.pair_policies += [{id:"poc-ab",name:"POC A then B",revision:0,priority:0,enabled:true,
    source:{peer_ids:[$a],group_ids:[]},destination:{peer_ids:[$b],group_ids:[]},
    constraints:{allow:{relay_ids:["relay-a","relay-b"],group_ids:[]},
      exclude:{relay_ids:[],group_ids:[]},force_relay:true,
      preferences:[{relay_ids:["relay-a"],group_ids:[]},{relay_ids:["relay-b"],group_ids:[]}]}}]' \
  configuration-before.json > configuration-candidate.json
curl --fail-with-body -sS -H "Authorization: Token $NB_POC_PAT" \
  -H 'Content-Type: application/json' --data-binary @configuration-candidate.json \
  "$NB_POC_API/relay-policies/preview" > configuration-preview.json
```

Review unsupported endpoints, blocked resolutions, contributors and affected
pairs. A truncated preview is incomplete evidence; narrow the scope. Do not reuse
`poc-ab` if that ID already exists. With explicit operator approval of the preview:

```bash
curl --fail-with-body -sS -X PUT -H "Authorization: Token $NB_POC_PAT" \
  -H 'Content-Type: application/json' --data-binary @configuration-candidate.json \
  "$NB_POC_API/relay-policies/configuration" > configuration-saved.json
```

Revision and catalog-generation conflicts require reload and a fresh preview;
never overwrite them with guessed values. Activation rejects assigned unsupported
endpoints and an empty effective relay set. Later capability loss or membership /
catalog changes can still leave an existing scope unsupported or blocked.

## Read desired state and actual observations

```bash
curl --fail-with-body -sS -G -H "Authorization: Token $NB_POC_PAT" \
  --data-urlencode "peer_a=$NB_POC_PEER_A" --data-urlencode "peer_b=$NB_POC_PEER_B" \
  "$NB_POC_API/relay-policies/status" > pair-status.json
jq '{peer_a,peer_b,policy,application,healthy,transport,relay_id,endpoints}' pair-status.json
curl --fail-with-body -sS -G -H "Authorization: Token $NB_POC_PAT" \
  --data-urlencode "peer_a=$NB_POC_PEER_A" \
  "$NB_POC_API/relay-policies/status" > home-status.json
```

Reversing pair IDs must return the same effective scope. Omitting `peer_b` reads
home status only. From each disposable client container also save
`netbird status -d` and `netbird status --json`; `relayPolicies` describes local
intent and observation without exposing relay credentials.

Reports normally refresh every **15 seconds** and expire after **45 seconds**.
Management authenticates the endpoint, current sync session, account/pair,
revision/digest, catalog/reselection generation and approved relay identity.
Latest observations live in shared SQL, so another Management receiver can read
them. A superseded session or stale owning instance invalidates them immediately.
Exact retry delivery cannot extend expiry; old sequences cannot renew health.

| Display | Interpretation |
| --- | --- |
| `pending` or missing endpoint | Not enough current observations to confirm application. |
| `unsupported` | An assigned endpoint lacks compatible enforcement capability. |
| `stale` | Expired, superseded-session or mismatched-policy observation. |
| `disagreement` | Endpoints differ on transport, relay ID or native attempt. |
| `applied`, `healthy: false` | Configuration converged, active transport unconfirmed; inspect bounded endpoint reasons such as `exhausted`. |
| `applied`, `healthy: true` | Current endpoints agree on an active path; still test ACL and application reachability. |
| `unconfigured` | No desired managed policy for this scope, not a client-clearance acknowledgment. |
| Desired `state: blocked` | No permitted active native relay; direct remains allowed only if force-relay is false. |

STUN availability proves neither relay authentication nor the pair's data path.
Home relay availability proves neither the pair's actual relay nor both-end
application. Prove the path with unique response bytes, matching endpoint status
and relay session/traffic evidence. Shared HMAC credentials authenticate relay
use, not a peer's claimed policy: enforcement is cooperative with compatible
clients. This does not provide adversary-resistant tenant isolation from modified
clients; ACLs and overlay encryption remain separate controls.

## Retry the reviewed preferred scope

After A recovers, snapshot the current pair policy and explicitly review its
target, preferred tiers and possible interruption before issuing:

```bash
jq -e '.policy.digest | strings | length > 0' pair-status.json
jq --arg a "$NB_POC_PEER_A" --arg b "$NB_POC_PEER_B" \
  '{peer_a:$a,peer_b:$b,expected_digest:.policy.digest}' pair-status.json > retry-pair.json
curl --fail-with-body -sS -H "Authorization: Token $NB_POC_PAT" \
  -H 'Content-Type: application/json' --data-binary @retry-pair.json \
  "$NB_POC_API/relay-policies/retry-preferred" > retry-result.json
```

Record the returned generation, then poll status for both matching observations
at that generation. The response acknowledges intent, not success. A changed
digest must trigger another review; a rate limit requires waiting. For home retry,
use the home status digest and omit `peer_b`. Do not infer pair recovery from it.

## Acceptance matrix and evidence

Run the automated package after U8 commits, following its README for supported
image overrides and exact test names. Retain the process exit status as well as
its output:

```bash
mkdir -p artifacts/relay-policy
set -o pipefail
go test -tags 'e2e devcert' -count=1 -timeout=45m -v ./e2e/relaypolicy \
  2>&1 | tee artifacts/relay-policy/acceptance.log
NB_POC_SUITE_EXIT=${PIPESTATUS[0]}
printf '%s\n' "$NB_POC_SUITE_EXIT" > artifacts/relay-policy/acceptance-exit.txt
```

Signal-reset fixtures use the separately controlled Signal container supplied by
`StartSignal`, with `WithSignalAddress` pointing Management at it. Stop/start only
that container to inject a signal outage; stopping the combined server would also
remove Management and would test a different failure. Save its logs and timestamps
alongside endpoint observations. Container-level signal resets approximate a
transport interruption; record separate proxy/gRPC reset injection when claiming
that specific deployment behavior.

The suite defaults to all three modes. To reproduce one mode, set
`NB_E2E_RELAY_MODES=kernel`, `userspace` or `netstack` (or a comma-separated list)
for that invocation, then record the reduced coverage in the evidence manifest.
`NB_E2E_COMBINED_IMAGE`, `NB_E2E_CLIENT_IMAGE` and `NB_E2E_RELAY_IMAGE` override
source-built images; pin them to reviewed immutable tags/digests. Do not point a
positive restrictive-path run at an old Management/client image that cannot
enforce its policy. `NB_E2E_BUILDX_CACHE` optionally names a local BuildKit cache
directory. Image overrides are fixtures, not version-based capability approval.
The controlled Docker relays use plain `rel://` only inside the disposable test
network. Passing that suite does not establish external TLS, certificates, QUIC,
public NAT behavior or physical LAN routing; exercise those separately when
claiming deployment acceptance.

This command alone is not proof of the complete matrix. Mark each row **passed**,
**failed**, or **not exercised**, with its topology/mode/map form and evidence.
Expand automated coverage with a separately documented disposable lab when a row
requires more infrastructure. Never relabel unexercised rows as passed.

| Scenario | Prerequisites and action | Required observations |
| --- | --- | --- |
| Forced A, control direct | Direct-capable pairs; only one force-relay rule | Marker bytes on both; target reports native A, unrelated pair stays direct. |
| Concurrent A/B pairs | One source and two remotes with different pins | Simultaneous responses and distinct pair relay IDs, not just home selection. |
| Regional home only | Separate home assignments, no restrictive pair rule | Cross-region application traffic still works; home and pair remain distinct. |
| A fails; A+B fail | Independently stop A, then B; C stays available | B carries bytes after A outage; after both fail no forbidden C path or unintended direct bypass. |
| Manual promotion / sticky recovery | Persistent B-first edit, then recover A | Preview affected pairs, record revision/interruption; B stays until explicit retry or a new failure. |
| Live exclusion | Active session on A, exclude A through owning rule | A ceases carrying the restricted pair; both endpoints converge or remain visibly pending/blocked. |
| Unsupported / offline / conflict | Older client, stopped peer, group membership conflict, retired catalog entry | Reject invalid activation; later unsupported/blocked/pending/stale states, never false healthy success. |
| Shared physical relays | Independent accounts/Management instances | Configuration stays account-scoped; unauthorized cross-account requests/traffic fail. |
| Overlapping customer addresses | Distinct database and HTTP markers behind isolated customer fixtures | Authorized flows reach only their expected marker; negative flows fail. Record sidecar versus real LAN scope. |
| Signal resets | Disrupt only disposable signal/proxy connections | Delayed convergence and stale/pending reports cannot become applied from old attempts. |
| Rolling catalog | Multiple Management replicas sharing SQL | One active generation, consistent effective digest, approved instance identity after replacement/retirement. |
| Targeted retry after recovery | Restore A with a working B path and an unrelated pair | Only selected scope generation changes; fresh matching reports and bytes show A or permitted fallback. |

For an additional manually driven disposable-lab run, take the container IDs from
that run's manifest. Never substitute a production relay. Use a responder URL
whose marker is known in advance; Alpine's BusyBox `wget` can collect HTTP bytes
without changing client images. For example, after setting
`NB_POC_RELAY_A_CONTAINER`, `NB_POC_RELAY_B_CONTAINER`,
`NB_POC_PEER_A_CONTAINER` and `NB_POC_HTTP_URL` from that manifest:

```bash
: "${NB_POC_RELAY_A_CONTAINER:?lab relay A container ID required}"
: "${NB_POC_RELAY_B_CONTAINER:?lab relay B container ID required}"
: "${NB_POC_PEER_A_CONTAINER:?lab peer container ID required}"
: "${NB_POC_HTTP_URL:?lab HTTP marker URL required}"
trap 'docker start "$NB_POC_RELAY_A_CONTAINER" "$NB_POC_RELAY_B_CONTAINER" > artifacts/relay-policy/restart.log 2>&1' EXIT
date -u +%Y-%m-%dT%H:%M:%SZ > artifacts/relay-policy/a-stop-time.txt
docker stop "$NB_POC_RELAY_A_CONTAINER" > artifacts/relay-policy/a-stop.log
if docker exec "$NB_POC_PEER_A_CONTAINER" sh -c 'wget -q -T 5 -O - "$1"' sh "$NB_POC_HTTP_URL"     > artifacts/relay-policy/after-a-response.txt 2> artifacts/relay-policy/after-a-error.txt; then
  echo 0 > artifacts/relay-policy/after-a-exit.txt
else
  echo "$?" > artifacts/relay-policy/after-a-exit.txt
fi
date -u +%Y-%m-%dT%H:%M:%SZ > artifacts/relay-policy/after-a-observation-time.txt
docker exec "$NB_POC_PEER_A_CONTAINER" netbird status --json   > artifacts/relay-policy/after-a-local-status.json
```

Repeat requests and both-end REST/local status capture across convergence, not
only this single request. Repeat with B stopped while C stays up for the negative
case, and then recover A/B. Compare exact expected marker bytes, actual relay IDs,
negative exit codes and matching generations. Do not infer successful recovery
from a zero exit code without checking the responder identity. Capture relay
session counters/logs for the same window; an open home registration or an
available STUN endpoint is insufficient. Leave container removal to the harness;
check its cleanup errors and the manual restart log before discarding the lab.

For each fault, record UTC injection, first failed request, first successful
response and first matching fresh endpoint reports. Publish measured interruption
and recovery durations only from those timestamps; no fixed failover SLA is
claimed. Save negative request exit codes and output, both endpoint reports and
relay evidence before/during/after. Exclude secrets and sensitive production
identifiers. Test kernel, userspace and netstack separately; one Linux-container
run is not evidence for every networking mode or both network-map forms.

## Roll out, clear and roll back

1. Back up the database and preserve the corresponding encryption key securely.
   Additive migrations are not a reversible rollback procedure.
2. Deploy compatible Management/Signal first, then supporting clients. Leave
   account policies empty during partial rollout. Preview capability coverage;
   activate only after every assigned endpoint supports the feature. Deploy the
   matching Dashboard to expose the feature/permission-gated controls.
3. Canary on disposable peers. Monitor pending, unsupported, stale, blocked and
   disagreement observations, reconnect/session growth and real traffic failures.
4. Before a downgrade that ignores policy, explicitly clear restrictive rules
   through the authorized API/UI, using the current revision and catalog
   generation and reviewing the broader impact. Clearing restores default relay
   behavior and is a security-relevant operator decision.
5. Keep compatible Management running until every affected endpoint has a fresh
   post-clear sync and local status proving the removed managed scope is absent.
   Capture that evidence from both ends and verify intended connectivity. Server
   `unconfigured` alone proves only desired state: there is no server-side
   tombstone acknowledgment for a removed policy in this branch.
6. Offline/disconnected endpoints prevent a claim of completed clearance. Keep
   compatible enforcement/control plane, reconnect and verify them before
   downgrading. Do not bypass the requirement by switching off enforcement while
   restrictive records remain. A future release switch must block new activation,
   not silently disable existing restrictions.
7. Let harness cleanup remove its disposable clients, relays and networks. If a
   disposable VM was configured manually, run `netbird down` inside that VM on
   every exit path. Never restore defaults by deleting live database rows or
   reversing migrations.
