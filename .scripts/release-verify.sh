#!/usr/bin/env bash
# Digest consistency only. The installer requires the owner's approved set digest.
set -euo pipefail
export LC_ALL=C
fail() { printf 'release verification: %s\n' "$1" >&2; exit 1; }
[[ $# == 2 && $2 =~ ^[0-9a-f]{64}$ ]] || fail 'usage: release-verify.sh ASSET_DIRECTORY EXPECTED_FRAGMENT_SHA256'
root=$(cd "$(dirname "$0")/.." && pwd -P)
cd "$1"
[[ -f release-set.json && ! -L release-set.json ]] || fail missing-or-symlinked-fragment
[[ $(sha256sum release-set.json) == "$2  release-set.json" ]] || fail fragment-digest-mismatch
python3 "$root/.scripts/release-metadata.py" check "$(pwd -P)"
printf 'Artifact digests match the supplied fragment digest; no origin or owner approval established.\n'
