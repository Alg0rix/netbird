# Fork upgrade guide for agents

Read this file before you merge upstream into this fork. Follow the steps in order. Do not skip a check.

## What this fork is

- Go repository: `Alg0rix/netbird`, branch `feat/relay-groups-pair-policies`. Upstream: `netbirdio/netbird` (git remote `upstream`).
- Dashboard repository: `Alg0rix/netbird-dashboard`, same branch name. Upstream: `netbirdio/dashboard` (git remote `origin`).
- The fork adds relay groups, home profiles, and pair policies. A peer or a pair of peers can prefer one relay and fall back to another.
- Fork code lives in new files and packages. See `docs/fork/patch-surface.md` for the upstream files the fork edits. Only those files can conflict.

## Rules

1. Never force push. Add a merge commit on top of the branch.
2. Never merge generated files by hand. Run `scripts/fork/regen.sh` (`*.pb.go`, `*.gen.go`).
3. Fork wire numbers use the reserved range. Proto field numbers and the capability enum value start at 1000. Activity codes start at 9000. Do not change an upstream number. Do not reuse a fork number.
4. If upstream adds a number in the reserved range, stop and ask the owner.
5. Keep upstream code as upstream wrote it. Re-apply the fork change on top. Do not keep the old fork version of an upstream file unless this guide says so.
6. Use SSH to push (`git@github.com:Alg0rix/netbird.git`). HTTPS is rejected when the merge adds a workflow file.
7. Do not commit `docs/plans/`. It is untracked on purpose.

## Go repository procedure

1. Run `scripts/fork/sync-upstream.sh check`. It tests the newest upstream release tag without changing files. Add `upstream/main` to test the dev branch.
2. If the result is clean, run `scripts/fork/sync-upstream.sh merge <ref>`. It merges, regenerates, builds, and runs the fork tests.
3. If conflicts remain, resolve them with the table below. Then run `scripts/fork/finish.sh`.
4. Review `git diff --cached --stat`. Commit with the message `Merge <ref> into feat/relay-groups-pair-policies` and a short body in simple English.
5. Run `scripts/fork/surface.sh` and commit `docs/fork/patch-surface.md` if it changed.
6. Push: `git push git@github.com:Alg0rix/netbird.git feat/relay-groups-pair-policies`.

### Conflict recipes

| File | How to resolve |
| --- | --- |
| `shared/relay/client/manager.go`, `picker.go`, `client.go`, `guard.go` | Take the upstream file as it is. Then re-add only the hook lines marked `// fork`: the embedded `policyState`, `trackPolicy`, `pickerPolicy`, and `clientPolicy`; the `m.initPolicy()` call in `NewManager`; and the `if m.policyEnabled` calls at the top of `Serve`, `OpenConn`, `RelayStates`, and `cleanUpUnusedRelays`. `guard.go` has no fork lines. |
| `shared/relay/client/client_policy.go` | `connectSessionWithLifetime` copies `Client.ConnectSession`. When upstream changes `ConnectSession`, apply the same change here. |
| `shared/relay/client/*_test.go` | Take the upstream test files as they are. Fork tests are in `manager_policy_test.go` and `registration_test.go`. They reuse the helpers of `manager_lifecycle_test.go`. |
| `client/internal/engine.go` | Take upstream. Re-add the lines marked `// fork`: the fields `relayPolicyDesired` and `relayHomeDesired`; `relayPolicyChanged` in `modifyPeers`; `decodeComponentNetworkMap` in `handleSync`; `streamCtx` and `startRelayPolicyReporter` in `receiveManagementEvents`; `applyRelayPolicies` and `failRelayPolicies` in `updateNetworkMap`; `relayPolicyUnusable` in `reconcilePeers`; the policy argument of `createPeerConn`; `RelayPolicyAttempt` in `convertToOfferAnswer`. The logic is in `relay_policy_apply.go`, `relay_policy_status.go`, `relay_policy_failure.go`. |
| `client/internal/peer/conn.go`, `worker_relay.go` | Take upstream. Re-add the `// fork` hooks: `RelayPolicy` in `ConnConfig`, the `connPolicy` embed, the force-relay check, `isCurrentConnection` in `onRelayConnectionIsReady`, and the `managed` hooks in `WorkerRelay`. The logic is in `conn_relay_policy.go` and `worker_relay_policy.go`. |
| `management/server/group.go` | Take upstream. In every group mutation, replace `am.Store.ExecuteInTransaction(ctx, func(transaction store.Store) error {` with `am.executeGroupTransaction(ctx, accountID, func(transaction store.Store) error {`. The nine functions are `CreateGroup`, `UpdateGroup`, `CreateGroups`, `updateSingleGroup`, `DeleteGroups`, `GroupAddPeer`, `GroupAddResource`, `GroupDeletePeer`, `GroupDeleteResource`. Do the same for any new upstream function that changes group members. |
| `management/server/store/sql_store_account.go` | Take upstream. Rename the upstream `GetAccount` to `getAccountOnce` (the fork `GetAccount` is in `sql_store_relay_account.go`). Re-add the relay preloads, the `attachRelayAccountSnapshot` calls in `getAccountGorm` and `getAccountPgx`, and `deleteRelayAccountConfig` in `DeleteAccount`. |
| `client/internal/peer/conn.go`, `management/internals/shared/grpc/server.go` | Keep both sides. These are usually additions in the same place. |
| `combined/cmd/config.go` | Take upstream imports. Add the `rp` import back. |
| `management/server/activity/codes.go` | Upstream owns the low numbers. Fork codes stay at 9000 and above. |
| `*.proto` | Keep both sides. Fork fields stay at 1000 and above. |
| `*.pb.go`, `*.gen.go` | Take either side. Run `scripts/fork/regen.sh` afterward. |
| `client/internal/relay_policy*.go`, `peer/relay_policy*.go` | If an upstream API changed, adapt the call. Do not change the policy rules. |

## Relay policy mode

The fork logic is in new files of `shared/relay/client`: `policy_state.go`, `manager_policy.go`, `picker_policy.go`, `client_policy.go`, `registration.go`, `home_policy.go`, `home_status.go`.
The manager runs it only when it is created with `WithRelayPolicy()`. The client engine does this in `client/internal/connect.go`. Without the option the manager is the upstream manager.
A policy call on a plain manager returns an error. If you see "relay policy mode is off", the manager was created without the option.

## Known test failures that are not caused by the fork

These fail on plain upstream `main`, or in a slow network. Ignore them.

- `shared/relay/client`: `TestServerPicker_UnavailableServers` fails when the resolver needs many seconds for an unknown host name.

- `management/internals/modules/agentnetwork`: tests about "private capabilities".
- `management/server`: `TestAgentNetwork_*` with the same message.
- `management/internals/network_map_db`: `TestRawJsonSupport`.

Any other failure is yours to fix.

## Checks that catch silent merge errors

- `shared/management/networkmap/fork_numbers_test.go` ties the three capability constants to the proto enum.
- `management/internals/modules/mcpapi` has a test that lists every public API route. New relay routes need an entry in `notExposed`.
- Duplicate map keys in `management/server/activity/codes.go` fail the build. That is how an activity code clash shows.

## Dashboard procedure

1. `git fetch origin` then `git merge origin/main`.
2. Known conflict: `src/layouts/Navigation.tsx`. Take upstream. Add back the Relays item after Peers: import `RadioTowerIcon`, import `useRelayPolicyConfiguration`, read `supported`, and show the item when `permission.relays?.read && supported`.
3. `npm ci`, then `npx tsc --noEmit -p .`.
4. Run `npx vitest run src/modules/relay-policies src/modules/relay-control src/modules/peer src/modules/navigation src/layouts`.
5. Build with `npm run build`. The build can stop once with a Next.js security notice. Run the same command again. Do not run `next upgrade` unless the owner asks.
6. Commit the merge. Push to `Alg0rix/netbird-dashboard`.

Dashboard fork code is in `src/modules/relay-control/` and `src/modules/relay-policies/`. It edits three upstream files: `src/layouts/Navigation.tsx`, `src/app/(dashboard)/peer/page.tsx`, `src/app/(dashboard)/relays/page.tsx`.

## Optional live check

The lab in `~/netbird-relay-failover-poc` has scripts that rebuild the images and run the slow-relay test. Use it after a large upstream change to the relay client, token authentication, or the network map.
