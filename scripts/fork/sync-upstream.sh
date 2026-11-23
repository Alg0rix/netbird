#!/usr/bin/env bash
# Bring an upstream ref into the fork branch.
#   sync-upstream.sh check [ref]   report conflicts without touching the working tree (safe for CI)
#   sync-upstream.sh merge [ref]   merge, regenerate, build, and run the fork tests
# ref defaults to the newest stable upstream release tag. Use "upstream/main" to follow the dev branch.
# Needs git 2.38 or newer for "check".
set -euo pipefail

cmd=${1:-check}
cd "$(git rev-parse --show-toplevel)"
git remote get-url upstream >/dev/null 2>&1 || git remote add upstream https://github.com/netbirdio/netbird.git
git fetch -q upstream --tags

ref=${2:-$(git tag --list 'v[0-9]*.[0-9]*.[0-9]*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -1)}
[ -n "$ref" ] || { echo "no upstream release tag found"; exit 2; }
echo "upstream ref: $ref ($(git rev-parse --short "$ref"))"
echo "behind: $(git rev-list --count HEAD.."$ref") commits"

case "$cmd" in
check)
  if out=$(git merge-tree --write-tree --name-only HEAD "$ref" 2>&1); then
    echo "clean: $ref merges without conflicts"
  else
    echo "CONFLICTS merging $ref:"
    echo "$out" | sed -n '2,$p' | grep -v '^$' | grep -v 'CONFLICT' | sort -u
    echo "see docs/fork/UPGRADE-GUIDE.md"
    exit 1
  fi
  ;;
merge)
  [ -z "$(git status --porcelain --untracked-files=no)" ] || { echo "working tree has changes; commit or stash first"; exit 2; }
  git config rerere.enabled true
  if ! git merge --no-commit --no-ff "$ref"; then
    echo "Conflicts. Resolve them using docs/fork/UPGRADE-GUIDE.md, then run: scripts/fork/finish.sh"
    exit 1
  fi
  "$(dirname "$0")/finish.sh"
  ;;
*)
  echo "usage: $0 check|merge [ref]"; exit 2 ;;
esac
