#!/usr/bin/env bash
# Finish an upstream merge: regenerate, build, test. Run after conflicts are resolved.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

if git ls-files -u | grep -q .; then echo "unresolved conflicts remain"; exit 1; fi
if git diff --cached --name-only | xargs -r grep -lE '^(<<<<<<<|>>>>>>>)' 2>/dev/null | grep -q .; then
  echo "conflict markers remain"; exit 1
fi

scripts/fork/regen.sh
# fork_numbers_test.go also guards the reserved number ranges
pkgs=$(go list -e ./... 2>/dev/null | grep -vE '/client/ui|magefiles|relay/test')
go build $pkgs
go test -count=1 -run XXX_NONE $pkgs >/dev/null   # every test package must compile
# TestServerPicker_UnavailableServers is an upstream test that dials unknown host names and fails when the resolver is slow
go test -count=1 -skip 'TestServerPicker_UnavailableServers' \
  ./shared/relay/... ./relay/... \
  ./shared/management/networkmap/... ./shared/management/relaypolicy/... \
  ./management/server/relaypolicy/... ./management/server/http/handlers/relay_policies/... \
  ./management/internals/shared/grpc/... ./management/internals/modules/mcpapi/... \
  ./client/internal/peer/... ./client/internal/
# group mutations and the account snapshot read carry fork hooks
go test -count=1 ./management/server/store/
go test -count=1 -run 'Group|Relay' ./management/server/
scripts/fork/surface.sh >/dev/null
echo "OK. Review 'git diff --cached --stat', then: git commit"
