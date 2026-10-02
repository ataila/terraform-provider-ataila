#!/usr/bin/env bash
# Copyright (c) 2026 ATAILA Kft.
# SPDX-License-Identifier: MPL-2.0
#
# Leak guard. This repository is mirrored publicly, so nothing internal may
# ever be committed to it: not in a file, not in a file name, not in a commit
# message, not in a commit that a later commit reverted.
#
# Scans
#   1. every tracked and untracked (not ignored) text file and file name;
#   2. the whole history of the checked-out branch: every added line of every
#      commit, every commit message and every author/committer identity.
#
# Fails on
#   - private IPv4 addresses (10/8, 172.16/12, 192.168/16);
#   - host names under any of the company's domains (`<host>.ataila.<tld>`),
#     but for the few public ones in PUBLIC_HOSTS below;
#   - Vault KV paths (the "secret" mount followed by a slash and a path);
#   - token shapes: platform API tokens, GitLab, GitHub and Vault tokens, private keys;
#   - internal code names.
# Examples and docs use portal.example.com, which none of these match.
#
# Usage: scripts/leak-guard.sh [--tree-only | --history-only] [--strict] [--rev <rev>]
# Exit:  0 clean, 1 leak found, 2 the guard itself could not run properly.
#
# Findings that are already in pushed history may be listed, by commit and by
# the sha256 of the exact finding (never the finding itself), in
# scripts/leak-guard-history.txt. A normal run reports them as KNOWN and passes;
# --strict fails on them. CI runs --strict on the default branch and on every
# tag, before anything is mirrored or released, so the list must stay empty:
# a finding in history means that history is rewritten before it is mirrored.
# A finding in the tree is never tolerated.
#
# Identities. The public history carries the organisation's identity only:
# every commit's author and committer address, and the tagger of every
# annotated tag in the history, is on IDENTITY_ALLOW, and every Co-Authored-By
# trailer names an address on COAUTHOR_ALLOW. --strict fails on any other
# address; a normal run reports it as IDENTITY and passes, so a branch warns
# before the default branch refuses.
#
# Before scanning, the guard proves each pattern still matches a sample built
# at run time, so a broken pattern fails loudly instead of passing silently.

set -euo pipefail

mode=all
rev=HEAD
strict=false
while [ $# -gt 0 ]; do
  case "$1" in
    --tree-only) mode=tree ;;
    --history-only) mode=history ;;
    --strict) strict=true ;;
    --rev) rev="${2:?--rev needs a revision}"; shift ;;
    -h|--help) sed -n '2,42p' "$0"; exit 0 ;;
    *) echo "leak-guard: unknown argument $1" >&2; exit 2 ;;
  esac
  shift
done

TAB=$'\t'
# category | grep flags | extended regular expression
PATTERNS=(
  "private address 10/8||(^|[^0-9.])10(\.[0-9]{1,3}){3}([^0-9]|$)"
  "private address 172.16/12||(^|[^0-9.])172\.(1[6-9]|2[0-9]|3[01])(\.[0-9]{1,3}){2}([^0-9]|$)"
  "private address 192.168/16||(^|[^0-9.])192\.168(\.[0-9]{1,3}){2}([^0-9]|$)"
  "company host name|-i|([a-z0-9-]+\.)+ataila\.[a-z]{2,63}"
  "Vault path||(^|[^A-Za-z0-9_.-])secret/[A-Za-z0-9_.-]+"
  "platform API token||ataila_(pat|sat)_[A-Za-z0-9_]{20,}"
  "GitLab token||glpat-[A-Za-z0-9_-]{20,}"
  "GitHub token||gh[pousr]_[A-Za-z0-9]{36,}|github_pat_[A-Za-z0-9_]{50,}"
  "Vault token||hv[sbr]\.[A-Za-z0-9_-]{20,}"
  "private key||-----BEGIN ([A-Z0-9]+ )*PRIVATE KEY( BLOCK)?-----"
  "internal code name|-i|lz[f]actory|landingzone[f]actory"
)
# Categories whose matches are never printed in full.
SECRET_CATEGORIES="platform API token|GitLab token|GitHub token|Vault token|private key"

# Documentation placeholders that may match a pattern above and are allowed.
# Exact strings, compared case-insensitively. Add an entry only with a reason.
ALLOW=(
)

# Public host names under the company's domains. These are published to every
# customer and visitor, so naming them leaks nothing: the company's websites,
# and the partner portal every customer signs in to, which the platform's API
# descriptions (the vendored contract) name. Exact host names, compared
# case-insensitively, that apply to the company host name pattern only; a name
# under one of them (`<x>.www.ataila.com`) is still a finding. Add an entry only
# for a name the company publishes, never one under portal, api, gitlab,
# harbor, vault or any other internal name: the self-test below refuses those.
# (The bare domains never match the pattern, which needs a host label; they are
# listed so that the list says everything that is public.)
PUBLIC_HOSTS=(
  www.ataila.com
  ataila.com
  # The website and the partner portal under the company's earlier website
  # domain, which the released history names (README, docs, the vendored
  # contract of earlier releases). Built from parts, so that no file of the
  # tree names that domain any more; allowed for that history only.
  "app.ataila"".eu"
  "www.ataila"".eu"
  "ataila"".eu"
)
# Labels a public host may never have.
INTERNAL_LABELS="portal|api|gitlab|harbor|vault|registry|git|dev|uat|prod|internal|admin|sso|auth|vpn|mail"

# The identities the public history may carry, as exact addresses compared
# case-insensitively (README, "Leak guard"). The history is published with
# every clone, so it carries the organisation's identity only: a personal
# address, or another organisation's, would be published with it. Authors,
# committers and taggers: the company address. Co-Authored-By trailers: the
# no-reply address of the AI assistant that co-writes the commits. Add an
# address only by a decision recorded in the README.
IDENTITY_ALLOW=(
  attila.macskasy@ataila.com
)
COAUTHOR_ALLOW=(
  noreply@anthropic.com
)

# public_host <finding>: the finding is exactly one of PUBLIC_HOSTS.
public_host() {
  local m="${1,,}" h
  for h in "${PUBLIC_HOSTS[@]}"; do
    [ "$m" = "${h,,}" ] && return 0
  done
  return 1
}

# allowed_address <list name> <address>: the address is on that list.
allowed_address() {
  local -n list="$1"
  local m="${2,,}" a
  [ -n "$m" ] || return 1
  for a in "${list[@]}"; do
    [ "$m" = "${a,,}" ] && return 0
  done
  return 1
}

die() { echo "leak-guard: $*" >&2; exit 2; }

command -v git >/dev/null || die "git is not installed"
top=$(git rev-parse --show-toplevel 2>/dev/null) || die "not inside a git repository"
cd "$top"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# ── self-test: every pattern must fire on a sample, and not on the negatives ──
samples=(
  "10.""20.30.40"
  "172.""20.3.4"
  "192.""168.1.20"
  "example-host.""ataila.com"
  "secret""/team/app/db"
  "ataila_""pat_abcd1234_0123456789abcdefghijklmnopqrstuvwxyzABCDEF"
  "glpat""-0123456789abcdefghij"
  "ghp""_0123456789abcdefghijklmnopqrstuvwxyzAB"
  "hvs"".CAESIJ0123456789abcdefghijk"
  "-----BEGIN ""OPENSSH PRIVATE KEY-----"
  "lz""factory"
)
negatives=(
  "portal.example.com"
  "127.0.0.1"
  "172.32.0.1"
  "192.0.2.10"
  "v1.10.2"
  "ataila_pat_…"
  "github_pat_…"
  "ghp_short"
  "-----BEGIN CERTIFICATE-----"
  "-----BEGIN PGP PUBLIC KEY BLOCK-----"
  "-----BEGIN PUBLIC KEY-----"
  "-----BEGIN PGP SIGNATURE-----"
  "terraform-provider-ataila.exe"
  "registry.opentofu.org/ataila/ataila"
  "github.com/ataila/terraform-provider-ataila"
)
for i in "${!PATTERNS[@]}"; do
  IFS='|' read -r name flags re <<<"${PATTERNS[$i]}"
  re="${PATTERNS[$i]#*|*|}"
  # Here-strings, not pipes: under pipefail an early-exiting grep -q could
  # make the writer die of SIGPIPE and turn a match into a failure.
  grep -qE $flags -- "$re" <<<"${samples[$i]}" \
    || die "self-test: pattern '$name' does not match its sample"
  for neg in "${negatives[@]}"; do
    if grep -qE $flags -- "$re" <<<"$neg"; then
      die "self-test: pattern '$name' matches the harmless '$neg'"
    fi
  done
done

# Every private key armour the guard must catch, built at run time so that
# this file holds none of them.
PRIVATE_KEY_HEADERS=(
  "-----BEGIN ""PGP PRIVATE KEY BLOCK-----"
  "-----BEGIN ""OPENSSH PRIVATE KEY-----"
  "-----BEGIN ""RSA PRIVATE KEY-----"
  "-----BEGIN ""EC PRIVATE KEY-----"
  "-----BEGIN ""DSA PRIVATE KEY-----"
  "-----BEGIN ""ENCRYPTED PRIVATE KEY-----"
  "-----BEGIN ""PRIVATE KEY-----"
)
for entry in "${PATTERNS[@]}"; do
  [[ "$entry" == "private key|"* ]] || continue
  re="${entry#*|*|}"
  for h in "${PRIVATE_KEY_HEADERS[@]}"; do
    grep -qE -- "$re" <<<"$h" || die "self-test: the private key pattern does not match '${h:0:16}…'"
  done
done

# ── self-test: the public host list holds public names only ─────────────────
for h in "${PUBLIC_HOSTS[@]}"; do
  grep -qiE -- '^([a-z0-9-]+\.)?ataila\.[a-z]{2,63}$' <<<"$h" \
    || die "self-test: public host '$h' is not a company domain or one host directly under it"
  if grep -qiE -- "(^|\.)($INTERNAL_LABELS)\." <<<"$h"; then
    die "self-test: public host '$h' names an internal service; it may not be on the list"
  fi
done
public_host "WWW.""Ataila.COM" || die "self-test: a public host in other letter case is not recognised"
for h in "x.www.""ataila.com" "www.""ataila.com.example" "example-host.""ataila.com"; do
  public_host "$h" && die "self-test: '$h' passes as a public host"
done

# ── self-test: the identity lists ───────────────────────────────────────────
allowed_address IDENTITY_ALLOW "${IDENTITY_ALLOW[0]^^}" || die "self-test: an allowed identity in other letter case is refused"
for a in "someone@example.com" "" "${COAUTHOR_ALLOW[0]}" "x${IDENTITY_ALLOW[0]}" "${IDENTITY_ALLOW[0]}.example"; do
  allowed_address IDENTITY_ALLOW "$a" && die "self-test: '$a' passes as an author, committer or tagger"
done
for a in "someone@example.com" "" "${IDENTITY_ALLOW[0]}"; do
  allowed_address COAUTHOR_ALLOW "$a" && die "self-test: '$a' passes in a Co-Authored-By trailer"
done

# ── build the streams: "<location><TAB><text>" per line ──────────────────────
streams=()

if [ "$mode" != history ]; then
  # File contents: tracked plus untracked-but-not-ignored, text only.
  # git grep exits 1 when nothing matched (no text files at all); 2+ is an error.
  rc=0
  git grep -I -n --untracked --full-name -e '' >"$work/tree.raw" || rc=$?
  [ "$rc" -le 1 ] || die "git grep failed ($rc)"
  sed -E "s/^([^:]*:[0-9]+):/\1${TAB}/" "$work/tree.raw" >"$work/tree"
  # File names.
  git ls-files --cached --others --exclude-standard | sed "s/^/file name${TAB}/" >"$work/names" \
    || die "git ls-files failed"
  files=$(git ls-files --cached --others --exclude-standard | wc -l | tr -d ' ')
  streams+=("$work/tree" "$work/names")
fi

if [ "$mode" != tree ]; then
  if [ "$(git rev-parse --is-shallow-repository)" = true ]; then
    die "the clone is shallow, so the history cannot be scanned. Fetch the full history (in GitLab CI: GIT_DEPTH: 0)."
  fi
  git rev-parse --verify --quiet "$rev^{commit}" >/dev/null || die "no such revision: $rev"
  marker="LEAKGUARD-COMMIT-7f3a9c"
  git log --no-color --no-ext-diff -p --format="${marker} %H%n%an <%ae>%n%cn <%ce>%n%B" "$rev" >"$work/log.raw" \
    || die "git log failed"
  awk -v marker="$marker" -v tab="$TAB" '
    index($0, marker " ") == 1 { sha = substr($2, 1, 12); inmsg = 1; file = ""; next }
    inmsg && index($0, "diff --git ") == 1 { inmsg = 0 }
    inmsg { print "commit " sha " message/identity" tab $0; next }
    index($0, "+++ ") == 1 { file = substr($0, 5); sub(/^b\//, "", file); print "commit " sha " file name" tab file; next }
    index($0, "+") == 1 { print "commit " sha " " file tab substr($0, 2) }
  ' "$work/log.raw" >"$work/history"
  commits=$(git rev-list --count "$rev")
  streams+=("$work/history")
fi

# ── scan ─────────────────────────────────────────────────────────────────────
allowed() {
  local m="${1,,}" a
  for a in "${ALLOW[@]+"${ALLOW[@]}"}"; do
    [ "$m" = "${a,,}" ] && return 0
  done
  return 1
}

mask() {
  local name="$1" m="$2"
  if [[ "|$SECRET_CATEGORIES|" == *"|$name|"* ]]; then
    printf '%s… (redacted, %d characters)' "${m:0:4}" "${#m}"
  else
    printf '%s' "$m"
  fi
}

# Known findings in pushed history: "<commit, 12 hex> <sha256 of the finding>".
KNOWN_FILE=scripts/leak-guard-history.txt
known_history=""
if [ -f "$KNOWN_FILE" ]; then
  known_history=$(grep -vE '^[[:space:]]*(#|$)' "$KNOWN_FILE" | awk '{ print $1 " " $2 }') \
    || known_history=""
fi
# known <location> <finding>: the finding is a listed one of that commit.
known() {
  local loc="$1" m="$2" sha hash
  [[ "$loc" == "commit "* ]] || return 1
  sha=$(printf '%s' "$loc" | awk '{ print $2 }')
  hash=$(printf '%s' "$m" | sha256sum | awk '{ print $1 }')
  grep -qxF -- "$sha $hash" <<<"$known_history"
}

found=0
tolerated=0
for entry in "${PATTERNS[@]}"; do
  IFS='|' read -r name flags _ <<<"$entry"
  re="${entry#*|*|}"
  for s in "${streams[@]}"; do
    { grep -E $flags -- "$re" "$s" || [ $? -eq 1 ]; } >"$work/hits" || die "grep failed on $s"
    while IFS= read -r line; do
      loc="${line%%"$TAB"*}"
      text="${line#*"$TAB"}"
      while IFS= read -r m; do
        m="$(printf '%s' "$m" | sed -E 's/^[^0-9A-Za-z_-]//; s/[^0-9A-Za-z_-]$//')"
        allowed "$m" && continue
        [ "$name" = "company host name" ] && public_host "$m" && continue
        if known "$loc" "$m"; then
          if [ "$strict" = true ]; then
            printf 'LEAK  %-26s %s: %s (listed in %s; rewrite this history)\n' "[$name]" "$loc" \
              "$(mask "$name" "$m")" "$KNOWN_FILE"
            found=1
          else
            printf 'KNOWN %-26s %s (listed in %s)\n' "[$name]" "$loc" "$KNOWN_FILE"
            tolerated=1
          fi
          continue
        fi
        printf 'LEAK  %-26s %s: %s\n' "[$name]" "$loc" "$(mask "$name" "$m")"
        found=1
      done < <(printf '%s\n' "$text" | grep -oE $flags -- "$re" || true)
    done <"$work/hits"
  done
done

# ── identities in history ───────────────────────────────────────────────────
identities=0
# identity <where> <address> <allowed>: one address that is not on its list.
identity() {
  if [ "$strict" = true ]; then
    printf 'LEAK  %-26s %s: %s (allowed: %s)\n' "[identity]" "$1" "${2:-no address}" "$3"
    found=1
  else
    printf 'IDENTITY %s: %s (allowed: %s; --strict refuses it)\n' "$1" "${2:-no address}" "$3"
    identities=1
  fi
}
if [ "$mode" != tree ]; then
  authors="${IDENTITY_ALLOW[*]}"
  coauthors="${COAUTHOR_ALLOW[*]}"
  # The recorded addresses, never a .mailmap's replacement for them.
  git log --no-use-mailmap --format="%H${TAB}%ae${TAB}%ce" "$rev" >"$work/idents" || die "git log failed"
  while IFS="$TAB" read -r sha ae ce; do
    allowed_address IDENTITY_ALLOW "$ae" || identity "commit ${sha:0:12} author" "$ae" "$authors"
    allowed_address IDENTITY_ALLOW "$ce" || identity "commit ${sha:0:12} committer" "$ce" "$authors"
  done <"$work/idents"
  git log --no-use-mailmap --format="${marker} %H%n%B" "$rev" >"$work/messages" || die "git log failed"
  awk -v marker="$marker" -v tab="$TAB" '
    index($0, marker " ") == 1 { sha = substr($2, 1, 12); next }
    tolower($0) ~ /^[ \t]*co-authored-by:/ { print sha tab $0 }
  ' "$work/messages" >"$work/trailers"
  while IFS="$TAB" read -r sha trailer; do
    addr=$(sed -nE 's/.*<([^>]*)>.*/\1/p' <<<"$trailer")
    allowed_address COAUTHOR_ALLOW "$addr" || identity "commit $sha Co-Authored-By" "$addr" "$coauthors"
  done <"$work/trailers"
  # Annotated tags in this history (a lightweight tag carries no identity).
  git for-each-ref --merged "$rev" --format="%(objecttype)${TAB}%(refname:short)${TAB}%(taggeremail)" refs/tags \
    >"$work/tags" || die "git for-each-ref failed"
  while IFS="$TAB" read -r type name email; do
    [ "$type" = tag ] || continue
    email="${email#<}"
    email="${email%>}"
    allowed_address IDENTITY_ALLOW "$email" || identity "tag $name tagger" "$email" "$authors"
  done <"$work/tags"
fi

summary="mode $mode"
[ "$strict" = true ] && summary+=", strict"
[ -n "${files:-}" ] && summary+=", $files files"
[ -n "${commits:-}" ] && summary+=", $commits commits of $rev"
if [ "$found" -ne 0 ]; then
  echo "leak-guard: FAILED ($summary). Remove the findings above; if one is in history, rewrite that history before anything is pushed or mirrored." >&2
  exit 1
fi
if [ "$identities" -ne 0 ]; then
  echo "leak-guard: the identities above may not reach the public history: re-author those commits (or re-create those tags) before they reach the default branch; --strict refuses them."
fi
if [ "$tolerated" -ne 0 ]; then
  echo "leak-guard: clean but for the KNOWN history findings above ($summary). The history must be rewritten before it is mirrored: --strict refuses them."
  exit 0
fi
echo "leak-guard: clean ($summary)"
