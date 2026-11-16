#!/usr/bin/env bash
# Build every Linux cmd main package with a fixed, pure-Go release configuration.
set -euo pipefail
export LC_ALL=C TZ=UTC GOTOOLCHAIN=local GOWORK=off GOENV=off
export CGO_ENABLED=0 GOOS=linux GOAMD64=v1 GOARM64=v8.0 GOEXPERIMENT=
export GOFLAGS='-mod=readonly -p=2'
fail() { printf 'release build: %s\n' "$1" >&2; exit 1; }
[[ ( $# == 1 || $# == 2 ) && $1 == /* ]] || fail absolute-new-output-directory-required
out=$1
go_bin=${GO:-go}
policy_root=$(cd "$(dirname "$0")/.." && pwd -P)
root=${2:-$(git rev-parse --show-toplevel)}
[[ $root == /* ]] || fail absolute-source-directory-required
cd "$root"
[[ -z $(git status --porcelain --untracked-files=all) ]] || fail dirty-build-source
commit=$(git rev-parse HEAD)
[[ $("$go_bin" env GOVERSION) == go1.26.10 ]] || fail go1.26.10-required
export SOURCE_DATE_EPOCH="$(git show -s --format=%ct HEAD)"
[[ $SOURCE_DATE_EPOCH =~ ^[0-9]+$ ]] || fail invalid-source-epoch
mkdir "$out"
printf 'Go: %s; SOURCE_DATE_EPOCH=%s\n' "$("$go_bin" env GOVERSION)" "$SOURCE_DATE_EPOCH"
for arch in amd64 arm64; do
  export GOARCH=$arch
  packages=$("$go_bin" list -f '{{if eq .Name "main"}}{{.ImportPath}}{{end}}' ./cmd/...)
  [[ -n $packages ]] || fail no-command-packages
  while IFS= read -r package; do
    [[ -n $package ]] || continue
    name=${package##*/}
    [[ $name =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ ]] || fail invalid-command-name
    binary="$out/${name}_linux_$arch"
    [[ ! -e $binary ]] || fail duplicate-command-name
    "$go_bin" build -trimpath -buildvcs=false -ldflags=-buildid= -o "$binary" "$package"
  done <<< "$packages"
done
cd "$out"
python3 "$policy_root/.scripts/release-metadata.py" create "$out" "$commit" \
  "$("$go_bin" env GOVERSION)" "$SOURCE_DATE_EPOCH"
sha256sum --strict --check SHA256SUMS
