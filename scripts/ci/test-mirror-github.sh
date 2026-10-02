#!/usr/bin/env bash
# Copyright (c) 2026 ATAILA Kft.
# SPDX-License-Identifier: MPL-2.0
#
# Tests scripts/ci/mirror-github.sh against a fake public repository (a bare
# repository on disk) with fake tags: the default branch is pushed, a vX.Y.Z
# tag of 1.0.0 or later is pushed, and a 0.x tag, a pre-release tag or another
# branch is refused with nothing pushed. Nothing leaves this machine.
#
# Usage: bash scripts/ci/test-mirror-github.sh   (run by the lint CI job)
set -euo pipefail

script="$(cd "$(dirname "$0")" && pwd)/mirror-github.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail() { echo "test-mirror-github: FAIL: $*" >&2; exit 1; }

git init -q --bare "$work/public.git"
git init -q -b main "$work/repo"
cd "$work/repo"
git config user.name "Mirror Test"
git config user.email "test@example.com"
git commit -q --allow-empty -m "first"
git tag -a v0.9.0 -m "a 0.x release"
git commit -q --allow-empty -m "second"
git tag -a v1.0.0 -m "the first public release"
git tag -a v1.1.0-rc1 -m "a pre-release"
head=$(git rev-parse HEAD)

export GITHUB_MIRROR_URL="file://$work/public.git" GITHUB_MIRROR_TOKEN=not-a-real-token CI_DEFAULT_BRANCH=main
public() { git --git-dir="$work/public.git" "$@"; }

# mirror <expected exit: 0 or fail> <env assignments...>
mirror() {
  local want="$1" rc=0
  shift
  # Only the variables given: a tag or branch pipeline running this test has
  # its own CI_COMMIT_* in the environment, which must not leak into a case.
  env -u CI_COMMIT_TAG -u CI_COMMIT_BRANCH -u CI_COMMIT_SHA -u MIRROR_FORCE "$@" bash "$script" >"$work/out" 2>&1 || rc=$?
  if [ "$want" = 0 ] && [ "$rc" -ne 0 ]; then fail "$* gave exit $rc: $(cat "$work/out")"; fi
  if [ "$want" = fail ] && [ "$rc" -eq 0 ]; then fail "$* was not refused: $(cat "$work/out")"; fi
}

mirror fail CI_COMMIT_TAG=v0.9.0
grep -q 'below 1.0.0' "$work/out" || fail "the 0.x refusal does not say why: $(cat "$work/out")"
mirror fail CI_COMMIT_TAG=v1.1.0-rc1
mirror fail CI_COMMIT_BRANCH=feature CI_COMMIT_SHA="$head"
[ -z "$(public tag --list)" ] || fail "a refused run pushed tags: $(public tag --list | tr '\n' ' ')"
public rev-parse --verify --quiet refs/heads/main >/dev/null && fail "a refused run pushed the default branch"

mirror 0 CI_COMMIT_BRANCH=main CI_COMMIT_SHA="$head"
[ "$(public rev-parse refs/heads/main)" = "$head" ] || fail "the default branch was not pushed"
mirror 0 CI_COMMIT_TAG=v1.0.0
[ "$(public tag --list)" = "v1.0.0" ] || fail "tags on the public repository: $(public tag --list | tr '\n' ' ')"

echo "test-mirror-github: ok (main and v1.0.0 pushed; v0.9.0, v1.1.0-rc1 and another branch refused)"
