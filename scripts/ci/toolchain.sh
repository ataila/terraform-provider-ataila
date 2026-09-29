#!/usr/bin/env bash
# Copyright (c) 2026 Macskásy Attila
# SPDX-License-Identifier: MPL-2.0
#
# Toolchain for CI jobs on a Linux shell runner. Source this file, then call
#
#   ensure_go                    Go $GO_VERSION on PATH
#   ensure_cli tofu 1.12.6       sets CLI_PATH to that OpenTofu binary
#   ensure_cli terraform 1.16.4  sets CLI_PATH to that Terraform binary
#   probe                        reports what the runner offers (never fails)
#
# Nothing is installed on the runner. Tools are downloaded into
# $TOOLCHAIN_DIR, inside the job's build directory and kept between jobs by
# the GitLab cache, and every archive is checked against the SHA-256 pinned
# below before it is unpacked. A runner therefore needs bash, curl, tar,
# sha256sum and unzip (or python3), plus HTTPS egress to:
#
#   go.dev, dl.google.com              the Go toolchain
#   proxy.golang.org, sum.golang.org   Go modules (or GOPROXY pointing at a mirror)
#   releases.hashicorp.com             Terraform
#   github.com and its release-asset host (objects/release-assets.githubusercontent.com)
#                                      OpenTofu
#
# A tool the runner already has at exactly the wanted version is used as is.
# When a download is impossible the job fails and says what is missing.

TOOLCHAIN_DIR="${TOOLCHAIN_DIR:-${CI_PROJECT_DIR:-$PWD}/.cache/tools}"

# Pinned SHA-256 of every archive CI may download (linux/amd64). Add a line
# when a version changes; a version without a line is refused.
_tc_sha256() {
  case "$1" in
    go1.27.1.linux-amd64.tar.gz)      echo 63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445 ;;
    terraform_1.6.0_linux_amd64.zip)  echo 0ddc3f21786026e1f8522ba0f5c6ed27a3c8cc56bfac91e342c1f578f8af44a8 ;;
    terraform_1.16.4_linux_amd64.zip) echo dc94af0eef1147718ad7c8daea792ed199e3e0492eec180d0adafa2a65a879df ;;
    tofu_1.6.0_linux_amd64.zip)       echo b96c3d1235bc4fd53b199175818a35642e50cbc6b82b8422dcab59240d06d885 ;;
    tofu_1.12.6_linux_amd64.zip)      echo 5dc43da4f750f33873dc25e94587128709e819e544b7be9016b255316153c3a8 ;;
    *) return 1 ;;
  esac
}

_tc_fail() {
  echo "" >&2
  echo "TOOLCHAIN ERROR: $*" >&2
  echo "" >&2
  exit 1
}

_tc_need() {
  local t
  for t in "$@"; do
    command -v "$t" >/dev/null 2>&1 || _tc_fail "'$t' is not installed on runner ${CI_RUNNER_DESCRIPTION:-$(hostname)}. An operator must install it."
  done
}

_tc_platform() {
  [ "$(uname -s)" = Linux ] || _tc_fail "this job needs a Linux runner, got $(uname -s)"
  [ "$(uname -m)" = x86_64 ] || _tc_fail "this job needs an x86_64 runner, got $(uname -m); pin the archives for this architecture in scripts/ci/toolchain.sh"
}

# _tc_fetch <url> <archive name> <what the runner must reach>
_tc_fetch() {
  local url="$1" name="$2" reach="$3" want got
  want=$(_tc_sha256 "$name") || _tc_fail "no pinned SHA-256 for $name: add one to scripts/ci/toolchain.sh"
  mkdir -p "$TOOLCHAIN_DIR/downloads"
  local out="$TOOLCHAIN_DIR/downloads/$name"
  if [ ! -f "$out" ]; then
    echo "toolchain: downloading $url" >&2
    if ! curl -fsSL --retry 3 --connect-timeout 15 --max-time 600 -o "$out.part" "$url"; then
      rm -f "$out.part"
      _tc_fail "cannot download $url from runner ${CI_RUNNER_DESCRIPTION:-$(hostname)}.
  The runner needs HTTPS egress to $reach,
  or the tool installed at exactly that version and on PATH."
    fi
    mv "$out.part" "$out"
  fi
  got=$(sha256sum "$out" | cut -d' ' -f1)
  if [ "$got" != "$want" ]; then
    rm -f "$out"
    _tc_fail "SHA-256 mismatch for $name: got $got, pinned $want. The download was altered or the pin is wrong."
  fi
  TC_ARCHIVE="$out"
}

_tc_unzip() {
  local zip="$1" dest="$2"
  mkdir -p "$dest"
  if command -v unzip >/dev/null 2>&1; then
    unzip -q -o "$zip" -d "$dest"
  elif command -v python3 >/dev/null 2>&1; then
    python3 -m zipfile -e "$zip" "$dest"
  else
    _tc_fail "neither unzip nor python3 is installed on runner ${CI_RUNNER_DESCRIPTION:-$(hostname)}"
  fi
}

ensure_go() {
  local want="${GO_VERSION:?GO_VERSION is not set}"
  export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"
  mkdir -p "$TOOLCHAIN_DIR"
  if command -v go >/dev/null 2>&1 && [ "$(go env GOVERSION 2>/dev/null)" = "go$want" ]; then
    echo "toolchain: using the runner's $(go version)"
  else
    _tc_platform
    _tc_need curl tar sha256sum
    local dir="$TOOLCHAIN_DIR/go-$want"
    if [ ! -x "$dir/go/bin/go" ]; then
      _tc_fetch "https://go.dev/dl/go$want.linux-amd64.tar.gz" "go$want.linux-amd64.tar.gz" "go.dev and dl.google.com"
      rm -rf "$dir" && mkdir -p "$dir"
      tar -xzf "$TC_ARCHIVE" -C "$dir"
    fi
    export PATH="$dir/go/bin:$PATH"
    echo "toolchain: $(go version)"
  fi
  if ! go mod download 2>"$TOOLCHAIN_DIR/.gomod.err"; then
    cat "$TOOLCHAIN_DIR/.gomod.err" >&2
    _tc_fail "cannot download Go modules through GOPROXY=$(go env GOPROXY).
  The runner needs HTTPS egress to proxy.golang.org and sum.golang.org,
  or GOPROXY (a CI variable) pointing at a reachable module mirror."
  fi
}

# ensure_cli <tofu|terraform> <version>: sets CLI_PATH.
ensure_cli() {
  local cli="$1" v="$2" name url reach
  case "$cli" in
    terraform)
      name="terraform_${v}_linux_amd64.zip"
      url="https://releases.hashicorp.com/terraform/$v/$name"
      reach="releases.hashicorp.com" ;;
    tofu)
      name="tofu_${v}_linux_amd64.zip"
      url="https://github.com/opentofu/opentofu/releases/download/v$v/$name"
      reach="github.com and its release-asset host" ;;
    *) _tc_fail "unknown CLI '$cli' (want tofu or terraform)" ;;
  esac

  local have
  have=$(command -v "$cli" 2>/dev/null || true)
  if [ -n "$have" ] && "$have" version 2>/dev/null | head -n1 | grep -qx "\(Terraform\|OpenTofu\) v$v"; then
    CLI_PATH="$have"
  else
    _tc_platform
    _tc_need curl sha256sum
    local dir="$TOOLCHAIN_DIR/$cli-$v"
    if [ ! -x "$dir/$cli" ]; then
      _tc_fetch "$url" "$name" "$reach"
      rm -rf "$dir"
      _tc_unzip "$TC_ARCHIVE" "$dir"
      chmod +x "$dir/$cli"
    fi
    CLI_PATH="$dir/$cli"
  fi
  local reported
  reported=$("$CLI_PATH" version | head -n1)
  case "$reported" in
    *" v$v") echo "toolchain: $reported at $CLI_PATH" ;;
    *) _tc_fail "$CLI_PATH reports '$reported', expected version $v" ;;
  esac
  export CLI_PATH
}

# probe: what the runner offers. Informational; always succeeds.
probe() {
  echo "== runner"
  echo "description: ${CI_RUNNER_DESCRIPTION:-?}"
  echo "id:          ${CI_RUNNER_ID:-?}"
  echo "tags:        ${CI_RUNNER_TAGS:-?}"
  echo "version:     ${CI_RUNNER_VERSION:-?} (${CI_RUNNER_EXECUTABLE_ARCH:-?})"
  echo "user:        $(id -un 2>/dev/null || echo ?)"
  echo "system:      $(uname -srm)"
  if [ -r /etc/os-release ]; then
    echo "os:          $(. /etc/os-release && echo "$PRETTY_NAME")"
  fi
  echo "docker:      $([ -S /var/run/docker.sock ] && echo 'socket present' || echo 'no socket')"
  echo
  echo "== tools on PATH (plus ~/.local/bin)"
  local PATH="$PATH:$HOME/.local/bin" t out
  for t in go terraform tofu git curl tar unzip python3 sha256sum gpg; do
    if command -v "$t" >/dev/null 2>&1; then
      case "$t" in
        go) out=$(go version 2>&1) ;;
        terraform|tofu) out=$("$t" version 2>&1 | head -n1) ;;
        *) out=$("$t" --version 2>&1 | head -n1) ;;
      esac
      printf '%-10s %s  (%s)\n' "$t" "$out" "$(command -v "$t")"
    else
      printf '%-10s absent\n' "$t"
    fi
  done
  echo
  echo "== HTTPS egress (HTTP status, 000 = unreachable)"
  local u code
  for u in https://go.dev/dl/ https://dl.google.com/ https://proxy.golang.org/ https://sum.golang.org/ \
           https://releases.hashicorp.com/ https://github.com/ https://objects.githubusercontent.com/ \
           https://release-assets.githubusercontent.com/ https://registry.terraform.io/ https://registry.opentofu.org/; do
    code=$(curl -s -o /dev/null -w '%{http_code}' --connect-timeout 8 --max-time 15 "$u" 2>/dev/null || true)
    printf '%-50s %s\n' "$u" "${code:-000}"
  done
  echo
  echo "== proxy settings"
  echo "HTTPS_PROXY=${HTTPS_PROXY:-${https_proxy:-unset}} NO_PROXY=${NO_PROXY:-${no_proxy:-unset}}"
  return 0
}
