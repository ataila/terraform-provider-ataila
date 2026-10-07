#!/usr/bin/env bash
# Copyright (c) 2026 Macskásy Attila (ATAILA)
# SPDX-License-Identifier: MPL-2.0
#
# One-way mirror of this repository to the public repository on GitHub, run by
# the mirror:github CI job after the leak guard and the strict leak guard
# passed on exactly the commit it pushes. It is a CI job and not a GitLab push
# mirror because a push mirror pushes before any pipeline has run: nothing
# could gate it.
#
#   On the default branch: pushes that commit to the public default branch.
#   On a vX.Y.Z tag:       pushes the tag (and the commits it needs); never
#                          a 0.x tag (1.0.0 is the first public release).
#
# It never forces: when the public history has diverged from GitLab's, the job
# fails and a person decides. A commit the public branch already holds (a
# pipeline that finished after a newer one) is no change.
#
# GITHUB_MIRROR_TOKEN (protected, masked): a fine-grained token with contents
# read and write on the public repository only; its value, or the path of a
# file holding it. Git reads it through a credential helper from the
# environment, so it is in no URL, no command line and no output.
set -euo pipefail

url="${GITHUB_MIRROR_URL:-https://github.com/ataila/terraform-provider-ataila.git}"
branch="${CI_DEFAULT_BRANCH:-main}"

token="${GITHUB_MIRROR_TOKEN:-}"
[ -n "$token" ] || { echo "mirror:github: GITHUB_MIRROR_TOKEN is not set" >&2; exit 1; }
if [ -f "$token" ]; then token=$(tr -d ' \r\n\t' <"$token"); fi
[ -n "$token" ] || { echo "mirror:github: GITHUB_MIRROR_TOKEN holds no token" >&2; exit 1; }
export MIRROR_TOKEN="$token"
unset token

# Only this helper, never one the runner has configured.
git_push() {
  git -c credential.helper= \
    -c credential.helper='!f() { test "$1" = get && printf "username=x-access-token\npassword=%s\n" "$MIRROR_TOKEN"; }; f' \
    -c credential.useHttpPath=false "$@"
}

if [ "$(git rev-parse --is-shallow-repository)" = true ]; then
  echo "mirror:github: the clone is shallow; set GIT_DEPTH: 0" >&2
  exit 1
fi

if [ -n "${CI_COMMIT_TAG:-}" ]; then
  [[ "$CI_COMMIT_TAG" =~ ^v([0-9]+)\.[0-9]+\.[0-9]+$ ]] \
    || { echo "mirror:github: $CI_COMMIT_TAG is not a release tag (vX.Y.Z); not mirrored" >&2; exit 1; }
  # 0.x releases were never public: 1.0.0 is the first public release.
  if [ "${BASH_REMATCH[1]}" -lt 1 ]; then
    echo "mirror:github: $CI_COMMIT_TAG is below 1.0.0; 0.x tags are never mirrored" >&2
    exit 1
  fi
  git rev-parse --verify --quiet "refs/tags/$CI_COMMIT_TAG" >/dev/null \
    || git fetch --quiet origin "refs/tags/$CI_COMMIT_TAG:refs/tags/$CI_COMMIT_TAG"
  echo "mirror:github: pushing tag $CI_COMMIT_TAG"
  git_push push --no-verify "$url" "refs/tags/$CI_COMMIT_TAG:refs/tags/$CI_COMMIT_TAG"
  exit 0
fi

sha="${CI_COMMIT_SHA:?CI_COMMIT_SHA is not set}"
[ "${CI_COMMIT_BRANCH:-}" = "$branch" ] || { echo "mirror:github: only $branch is mirrored" >&2; exit 1; }
# What the public branch holds now (nothing, for an empty repository).
if git_push fetch --quiet --no-tags "$url" "+refs/heads/$branch:refs/mirror/public" 2>/dev/null; then
  if git merge-base --is-ancestor "$sha" refs/mirror/public; then
    echo "mirror:github: the public $branch already holds ${sha:0:12}; nothing to push"
    exit 0
  fi
  if ! git merge-base --is-ancestor refs/mirror/public "$sha"; then
    echo "mirror:github: the public $branch has diverged from GitLab's; refusing to force. A person must decide." >&2
    exit 1
  fi
fi
echo "mirror:github: pushing ${sha:0:12} to the public $branch"
git_push push --no-verify "$url" "$sha:refs/heads/$branch"
