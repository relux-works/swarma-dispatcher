#!/usr/bin/env bash
# Optional bot checksum signature after an independent build; no origin authority.
set +x
set -euo pipefail
export LC_ALL=C GIT_NO_REPLACE_OBJECTS=1
umask 077
fail() { printf 'release signing: %s\n' "$1" >&2; exit 1; }
[[ $# == 1 || $# == 2 ]] || fail 'usage: release-sign.sh TAG [OWNER/REPO]'
tag=$1
repo=${2:-relux-works/swarma-dispatcher}
[[ $tag =~ ^v[0-9][0-9A-Za-z.+-]*$ ]] || fail invalid-tag
[[ $repo =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]] || fail invalid-repository
root=$(cd "$(dirname "$0")/.." && pwd -P)
cd "$root"
# Fetch without overwriting a moved local tag. Admission executes only main's policy.
git fetch origin refs/heads/main:refs/remotes/origin/main "refs/tags/$tag:refs/tags/$tag"
admitted=$("$root/.scripts/release-admit.sh" "$tag")
commit=$(sed -n 's/^commit=//p' <<< "$admitted")
tag_object=$(sed -n 's/^tag_object=//p' <<< "$admitted")
key=$(git config --get user.signingkey)
[[ -n $key ]] || fail missing-configured-key
go_bin=$(command -v "${GO:-go}")
snapshot() {
  gh release view "$tag" --repo "$repo" --json databaseId,isDraft,isPrerelease,tagName,body \
    --jq '[.databaseId, .isDraft, .isPrerelease, .tagName, (.body | contains("agent-held key, proves nothing about origin"))] | @tsv'
}
initial=$(snapshot)
IFS=$'\t' read -r release_id draft prerelease release_tag labelled <<< "$initial"
[[ $release_id =~ ^[0-9]+$ && $draft == true && $prerelease == true && $release_tag == "$tag" && $labelled == true ]] || fail labelled-prerelease-draft-required
mkdir -p "$root/.temp"
work=$(mktemp -d "$root/.temp/release-sign.XXXXXX")
work=$(cd "$work" && pwd -P)
printf 'Rebuild/signing evidence: %s\n' "$work"
mkdir -p "$work"/{home,config,cache,tmp,go-build,go-mod,go-path,download,readback}
# Resolve the key/tool before isolating HOME. Never run tag-provided scripts.
(
  export HOME="$work/home" USERPROFILE="$work/home"
  export XDG_CONFIG_HOME="$work/config" XDG_CACHE_HOME="$work/cache"
  export TMPDIR="$work/tmp" TMP="$work/tmp" TEMP="$work/tmp" GOTMPDIR="$work/tmp"
  export GOCACHE="$work/go-build" GOMODCACHE="$work/go-mod" GOPATH="$work/go-path"
  unset GOTELEMETRYDIR
  # Local object copy with a fresh index and no inherited build configuration.
  git clone --quiet --no-local --no-checkout "$root" "$work/source"
  git -C "$work/source" checkout --quiet --detach "$commit"
  GO="$go_bin" "$root/.scripts/release-build.sh" "$work/dist" "$work/source"
)
gh release download "$tag" --repo "$repo" --pattern SHA256SUMS \
  --pattern provenance.json --pattern release-set.json --dir "$work/download"
# Byte equality of the canonical list binds every artifact digest, including provenance.
for asset in SHA256SUMS provenance.json release-set.json; do
  cmp "$work/dist/$asset" "$work/download/$asset" || fail rebuild-digest-mismatch
done
[[ $(snapshot) == "$initial" ]] || fail draft-changed
[[ $(git ls-remote --refs origin "refs/tags/$tag") == "$tag_object"$'\t'"refs/tags/$tag" ]] || fail remote-tag-changed
[[ $(git ls-remote --refs origin refs/heads/main) == "$(git rev-parse HEAD)"$'\trefs/heads/main' ]] || fail remote-main-changed
"$root/.scripts/release-admit.sh" "$tag"
# Sign the locally rebuilt list, never the downloaded file. No allowlist for files.
ssh-keygen -Y sign -n file -f "$key" "$work/dist/SHA256SUMS"
# No --clobber: an existing signature requires operator reconciliation.
gh release upload "$tag" "$work/dist/SHA256SUMS.sig" --repo "$repo"
gh release download "$tag" --repo "$repo" --pattern SHA256SUMS \
  --pattern SHA256SUMS.sig --pattern provenance.json --pattern release-set.json --dir "$work/readback"
for asset in SHA256SUMS SHA256SUMS.sig provenance.json release-set.json; do
  cmp "$work/dist/$asset" "$work/readback/$asset" || fail readback-mismatch
done
[[ $(snapshot) == "$initial" ]] || fail draft-changed
printf 'agent-held key, proves nothing about origin\nRelease remains a pre-release draft.\n'
