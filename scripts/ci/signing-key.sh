#!/usr/bin/env bash
# Copyright (c) 2026 Macskásy Attila
# SPDX-License-Identifier: MPL-2.0
#
# Imports the release signing key into $GNUPGHOME and checks it is one the
# registries accept, before goreleaser builds anything. Run by the release job:
#
#   export GNUPGHOME="$(mktemp -d)"; bash scripts/ci/signing-key.sh
#
# The key comes from two protected CI/CD variables (README, "CI and releases"):
#
#   GPG_PRIVATE_KEY  type File, protected and masked: the private key, exported
#                    without a passphrase, either ASCII-armored or as that
#                    armored text base64-encoded on one line. GitLab masks a
#                    value only when it is one line, so store the base64 form.
#   GPG_FINGERPRINT  protected: the key's full fingerprint.
#
# The OpenTofu and Terraform registries verify signatures made with RSA or DSA
# keys only (an ECC key, ed25519 included, is refused at publication): use an
# RSA key of 4096 bits. Protect the v* tags, or the protected variables never
# reach a tag pipeline.
#
# Exits 1 with a "RELEASE STOPPED" message naming what is missing or wrong.
# Never prints the key.
set -euo pipefail

stop() {
  echo "RELEASE STOPPED: $1"
  shift
  local line
  for line in "$@"; do echo "  $line"; done
  echo "  See README, \"CI and releases\": an RSA-4096 key without a passphrase in the protected,"
  echo "  masked File variable GPG_PRIVATE_KEY (one-line base64 of the armored key), its"
  echo "  fingerprint in the protected variable GPG_FINGERPRINT, and protected v* tags."
  exit 1
}

if [ -z "${GPG_PRIVATE_KEY:-}" ] || [ -z "${GPG_FINGERPRINT:-}" ]; then
  stop "no signing key." \
    "The protected CI/CD variables GPG_PRIVATE_KEY and GPG_FINGERPRINT are not both set for" \
    "this pipeline, so nothing was built: the registries accept only signed releases. Either" \
    "they do not exist yet, or this tag is not protected (protected variables reach protected" \
    "refs only). An operator adds both, protects the v* tags and re-runs this job."
fi
command -v gpg >/dev/null || stop "gpg is not installed on this runner."
[ -n "${GNUPGHOME:-}" ] && [ -d "$GNUPGHOME" ] || stop "GNUPGHOME is not a directory (the job sets a fresh one)."

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# The variable is a File variable (a path) or, set as a plain variable, the value.
if [ -f "$GPG_PRIVATE_KEY" ]; then
  cp "$GPG_PRIVATE_KEY" "$work/key"
else
  printf '%s\n' "$GPG_PRIVATE_KEY" >"$work/key"
fi
if grep -q -- '-----BEGIN PGP ' "$work/key"; then
  cp "$work/key" "$work/key.asc"
elif ! tr -d ' \r\n\t' <"$work/key" | base64 -d >"$work/key.asc" 2>/dev/null \
  || ! grep -q -- '-----BEGIN PGP ' "$work/key.asc"; then
  stop "GPG_PRIVATE_KEY holds neither an armored key nor its base64 encoding."
fi
gpg --batch --quiet --import "$work/key.asc" 2>"$work/import.log" \
  || stop "gpg could not import GPG_PRIVATE_KEY." "$(grep -v -i 'secret key' "$work/import.log" | head -n 3)"

fpr=$(printf '%s' "$GPG_FINGERPRINT" | tr -d ' ' | tr 'a-f' 'A-F')
gpg --batch --with-colons --list-secret-keys "$fpr" >"$work/keys" 2>/dev/null \
  || stop "GPG_PRIVATE_KEY holds no secret key with the fingerprint in GPG_FINGERPRINT."

# Every key that can sign (capability "s") must be RSA (1, 3) or DSA (17).
bad=$(awk -F: '($1 == "sec" || $1 == "ssb") && $12 ~ /s/ && $4 != 1 && $4 != 3 && $4 != 17 { print $4 }' "$work/keys")
if [ -n "$bad" ]; then
  stop "the signing key is not RSA or DSA (OpenPGP algorithm $(echo $bad | tr ' ' ','))." \
    "The registries verify RSA and DSA signatures only. Generate an RSA-4096 key."
fi
size=$(awk -F: '$1 == "sec" { print $3; exit }' "$work/keys")
[ "${size:-0}" -ge 4096 ] || echo "Note: the signing key has ${size:-?} bits; RSA-4096 is recommended."

# A key with a passphrase cannot sign here: nothing could type it.
echo "signing check" >"$work/probe"
gpg --batch --pinentry-mode error --local-user "$fpr" --detach-sign --output "$work/probe.sig" "$work/probe" \
  2>/dev/null || stop "the signing key cannot sign without a passphrase." \
  "Export it without one (the protected, masked variable is its protection)."
echo "signing key: RSA/DSA, ${size:-?} bits, fingerprint ending ${fpr: -16}, imported"
