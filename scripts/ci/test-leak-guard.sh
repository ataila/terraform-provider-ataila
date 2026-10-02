#!/usr/bin/env bash
# Copyright (c) 2026 Macskásy Attila
# SPDX-License-Identifier: MPL-2.0
#
# Tests scripts/leak-guard.sh on a scratch repository: every private key
# armour planted in a file, and in a commit that a later commit removed, is a
# finding; a public key block (as docs/signing-key.asc holds) is not.
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

echo "test-leak-guard: ok (${#headers[@]} private key armours found in the tree and in history; a public key block passes)"
