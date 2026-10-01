#!/usr/bin/env bash
# Copyright (c) 2026 Macskásy Attila
# SPDX-License-Identifier: MPL-2.0
#
# The acceptance tests, split by domain so that each CI job stays short: one
# job per CLI version and domain.
#
# Usage: scripts/ci/acc-domains.sh <domain>   prints the domain's -run regexp
#        scripts/ci/acc-domains.sh --list     prints the domain names
#        scripts/ci/acc-domains.sh --check    fails unless every acceptance
#                                             test is in exactly one domain
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
    [ "$bad" -eq 0 ] || exit 1
    echo "acc-domains: $(wc -l <<<"$tests" | tr -d ' ') acceptance tests, each in exactly one of ${#DOMAINS[@]} domains"
    ;;
  "")
    echo "usage: $0 <domain> | --list | --check" >&2
    exit 2
    ;;
  *)
    [ -n "${DOMAINS[$1]+set}" ] || { echo "acc-domains: no domain $1" >&2; exit 2; }
    printf '%s\n' "${DOMAINS[$1]}"
    ;;
esac
