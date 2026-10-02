#!/usr/bin/env bash
# Copyright (c) 2026 ATAILA Kft.
# SPDX-License-Identifier: MPL-2.0
#
# The acceptance tests, split by domain so that each CI job stays short. A CI
# job runs one shard: one domain, or several joined with "+"
# (tenancy+users-gateway), so that the number of jobs can follow the runners
# without moving any test.
#
# Usage: scripts/ci/acc-domains.sh <shard>   prints the shard's -run regexp
#        scripts/ci/acc-domains.sh --list    prints the domain names
#        scripts/ci/acc-domains.sh --check   fails unless every acceptance
#                                            test is in exactly one domain and
#                                            every ACC_DOMAIN list of the CI
#                                            matrix names every domain once
set -euo pipefail

declare -A DOMAINS=(
  [tenancy]='^TestAcc(Customer|Tenant|Timestamp|Provider|Meta|Whoami)'
  [users-gateway]='^TestAcc(User|Gateway|Serving)'
  [projects-releases]='^TestAcc(Project|Release)'
  [ai-licence-brand]='^TestAcc(AI|Licence|Brand)'
)

case "${1:-}" in
  --list)
    printf '%s\n' "${!DOMAINS[@]}" | sort
    ;;
  --check)
    tests=$(go test ./internal/provider/ -list '^TestAcc' | grep '^TestAcc' || true)
    [ -n "$tests" ] || { echo "acc-domains: no acceptance tests listed" >&2; exit 2; }
    bad=0
    while IFS= read -r t; do
      n=0
      for d in "${!DOMAINS[@]}"; do
        if grep -qE -- "${DOMAINS[$d]}" <<<"$t"; then n=$((n + 1)); fi
      done
      if [ "$n" -ne 1 ]; then
        echo "acc-domains: $t is in $n domains, not exactly one" >&2
        bad=1
      fi
    done <<<"$tests"
    # The CI matrix: every ACC_DOMAIN list names each domain exactly once.
    ci="$(git rev-parse --show-toplevel)/.gitlab-ci.yml"
    want=$(printf '%s\n' "${!DOMAINS[@]}" | sort | tr '\n' ' ')
    lists=$(sed -nE 's/^[[:space:]]*ACC_DOMAIN:[[:space:]]*\[(.*)\][[:space:]]*$/\1/p' "$ci")
    [ -n "$lists" ] || { echo "acc-domains: no ACC_DOMAIN list in $ci" >&2; exit 2; }
    while IFS= read -r l; do
      got=$(tr ',+' '\n\n' <<<"$l" | tr -d ' "'"'" | grep -v '^$' | sort | tr '\n' ' ')
      if [ "$got" != "$want" ]; then
        echo "acc-domains: the CI matrix list [$l] does not name every domain exactly once" >&2
        bad=1
      fi
    done <<<"$lists"
    [ "$bad" -eq 0 ] || exit 1
    echo "acc-domains: $(wc -l <<<"$tests" | tr -d ' ') acceptance tests, each in exactly one of ${#DOMAINS[@]} domains;" \
      "$(wc -l <<<"$lists" | tr -d ' ') CI matrix lists, each naming every domain once"
    ;;
  "" | -*)
    echo "usage: $0 <domain>[+<domain>...] | --list | --check" >&2
    exit 2
    ;;
  *)
    run=""
    IFS='+' read -r -a shard <<<"$1"
    for d in "${shard[@]}"; do
      [ -n "${DOMAINS[$d]+set}" ] || { echo "acc-domains: no domain $d" >&2; exit 2; }
      run="${run:+$run|}${DOMAINS[$d]}"
    done
    printf '%s\n' "$run"
    ;;
esac
