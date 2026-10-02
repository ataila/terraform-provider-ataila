#!/usr/bin/env bash
# Copyright (c) 2026 ATAILA Kft.
# SPDX-License-Identifier: MPL-2.0
#
# Tests scripts/leak-guard.sh on scratch repositories: every private key
# armour planted in a file, and in a commit that a later commit removed, is a
# finding; a public key block (as docs/signing-key.asc holds) is not; under
# --strict, an author, committer, co-author or tagger address off the guard's
# lists is a finding.
#
# Usage: bash scripts/ci/test-leak-guard.sh   (run by the leak-guard CI job)
set -euo pipefail

guard="$(cd "$(dirname "$0")/.." && pwd)/leak-guard.sh"
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

fail() { echo "test-leak-guard: FAIL: $*" >&2; exit 1; }

# Built at run time, so that this file holds none of them.
headers=(
  "-----BEGIN ""PGP PRIVATE KEY BLOCK-----"
  "-----BEGIN ""OPENSSH PRIVATE KEY-----"
  "-----BEGIN ""RSA PRIVATE KEY-----"
  "-----BEGIN ""EC PRIVATE KEY-----"
  "-----BEGIN ""PRIVATE KEY-----"
)

cd "$work"
git init -q -b main .
git config user.name "Leak Guard Test"
git config user.email "test@example.com"
printf '%s\n' "-----BEGIN ""PGP PUBLIC KEY BLOCK-----" "mQINBGc0" "-----END ""PGP PUBLIC KEY BLOCK-----" >public.asc
git add public.asc
git commit -q -m "a public key"

# 1. A public key block is no finding.
bash "$guard" >"$work/out" 2>&1 || fail "a public key block was reported: $(cat "$work/out")"

# 2. Each private key header in the tree is a finding, and is never printed.
printf '%s\n' "${headers[@]}" >keys.txt
rc=0
bash "$guard" --tree-only >"$work/out" 2>&1 || rc=$?
[ "$rc" -eq 1 ] || fail "the planted keys gave exit $rc, want 1: $(cat "$work/out")"
n=$(grep -c '^LEAK  \[private key\]' "$work/out" || true)
[ "$n" -eq "${#headers[@]}" ] || fail "$n private key findings, want ${#headers[@]}: $(cat "$work/out")"
if grep -q 'PRIVATE KEY' "$work/out"; then
  fail "a finding printed the key armour"
fi

# 3. The same headers committed and removed again are findings in history.
git add keys.txt
git commit -q -m "keys"
git rm -q keys.txt
git commit -q -m "keys removed"
rc=0
bash "$guard" --history-only >"$work/out" 2>&1 || rc=$?
[ "$rc" -eq 1 ] || fail "the keys in history gave exit $rc, want 1: $(cat "$work/out")"
n=$(grep -c '^LEAK  \[private key\]' "$work/out" || true)
[ "$n" -eq "${#headers[@]}" ] || fail "$n private key findings in history, want ${#headers[@]}"

# 4. Identities, in a second scratch repository: the organisation's address as
#    author, committer and tagger, and the assistant's in Co-Authored-By, pass
#    --strict; any other address in one of those places fails it, and a normal
#    run reports it and passes.
org="attila.macskasy@ataila.com"
assistant="noreply@anthropic.com"
other="someone@example.com"
mkdir "$work/ident"
cd "$work/ident"
git init -q -b main .
git config user.name "Leak Guard Test"
git config user.email "$org"
echo one >file.txt
git add file.txt
git commit -q -m "one" -m "Co-Authored-By: Assistant <$assistant>"
git tag -a v1.0.0 -m "v1.0.0"
good=$(git rev-parse HEAD)
rc=0
bash "$guard" --strict --history-only >"$work/out" 2>&1 || rc=$?
[ "$rc" -eq 0 ] || fail "the allowed identities gave exit $rc, want 0: $(cat "$work/out")"

# expect_identity <what>: --strict fails with one identity finding naming it,
# a normal run passes and reports it; then back to the good commit.
expect_identity() {
  rc=0
  bash "$guard" --strict --history-only >"$work/out" 2>&1 || rc=$?
  [ "$rc" -eq 1 ] || fail "$1: --strict gave exit $rc, want 1: $(cat "$work/out")"
  n=$(grep -c '^LEAK  \[identity\]' "$work/out" || true)
  [ "$n" -eq 1 ] || fail "$1: $n identity findings, want 1: $(cat "$work/out")"
  grep -q "$1" "$work/out" || fail "$1: the finding does not say where: $(cat "$work/out")"
  rc=0
  bash "$guard" --history-only >"$work/out" 2>&1 || rc=$?
  [ "$rc" -eq 0 ] || fail "$1: a normal run gave exit $rc, want 0: $(cat "$work/out")"
  grep -q '^IDENTITY ' "$work/out" || fail "$1: a normal run did not report it: $(cat "$work/out")"
  git tag -d v9.9.9 >/dev/null 2>&1 || true
  git reset -q --hard "$good"
}

echo two >>file.txt
GIT_AUTHOR_EMAIL="$other" git commit -q -am "another author"
expect_identity "author"
echo two >>file.txt
GIT_COMMITTER_EMAIL="$other" git commit -q -am "another committer"
expect_identity "committer"
echo two >>file.txt
git commit -q -am "another co-author" -m "Co-Authored-By: Someone <$other>"
expect_identity "Co-Authored-By"
echo two >>file.txt
git commit -q -am "a co-author without an address" -m "co-authored-by: Someone"
expect_identity "Co-Authored-By"
GIT_COMMITTER_EMAIL="$other" git tag -a v9.9.9 -m "v9.9.9"
expect_identity "tagger"

echo "test-leak-guard: ok (${#headers[@]} private key armours found in the tree and in history; a public key block passes; another author, committer, co-author or tagger fails --strict)"
