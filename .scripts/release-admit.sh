#!/usr/bin/env bash
# Run this policy from the default-branch checkout, never from the tagged tree.
set -euo pipefail
export LC_ALL=C GIT_NO_REPLACE_OBJECTS=1
fail() { printf 'release admission: %s\n' "$1" >&2; exit 1; }
[[ $# == 1 && $1 =~ ^v[0-9][0-9A-Za-z.+-]*$ ]] || fail invalid-tag
tag=$1
ref="refs/tags/$tag"
root=$(cd "$(dirname "$0")/.." && pwd -P)
cd "$root"
git() { command git -c core.commitGraph=false "$@"; }
[[ $(git rev-parse --is-shallow-repository) == false ]] || fail shallow-history
grafts=$(git rev-parse --git-path info/grafts)
[[ ! -e $grafts && ! -L $grafts ]] || fail grafted-history
[[ $(git rev-parse HEAD) == "$(git rev-parse refs/remotes/origin/main)" ]] || fail default-checkout-required
policy=(.scripts .github/release .github/workflows/release.yml)
[[ -z $(git status --porcelain --untracked-files=all -- "${policy[@]}") ]] || fail dirty-release-policy
[[ $(git cat-file -t "$ref") == tag ]] || fail annotated-tag-required
raw=$(git cat-file tag "$ref")
header=${raw%%$'\n\n'*}
[[ $'\n'"$header"$'\n' == *$'\ntype commit\n'* ]] || fail direct-commit-required
[[ $'\n'"$header"$'\n' == *$'\ntag '"$tag"$'\n'* ]] || fail tag-name-mismatch
[[ $raw == *$'\n-----BEGIN SSH SIGNATURE-----\n'* ]] || fail ssh-signature-required
tag_object=$(git rev-parse "$ref")
commit=$(git rev-parse "$ref^{commit}")
[[ -z ${RELEASE_TAG_OBJECT:-} || $RELEASE_TAG_OBJECT == "$tag_object" ]] || fail tag-object-mismatch
[[ -z ${RELEASE_COMMIT:-} || $RELEASE_COMMIT == "$commit" ]] || fail commit-mismatch
git merge-base --is-ancestor "$commit" refs/remotes/origin/main || fail commit-not-on-main
# Old or modified release controls cannot widen admission, even on a main ancestor.
git diff --quiet refs/remotes/origin/main "$commit" -- "${policy[@]}" || fail tagged-release-policy-differs
# The public pin is a tag consistency check only, never a product trust root.
git -c gpg.format=ssh -c gpg.ssh.program=ssh-keygen \
  -c gpg.openpgp.program=false -c gpg.x509.program=false \
  -c "gpg.ssh.allowedSignersFile=$root/.github/release/allowed_signers" \
  verify-tag "$ref" || fail inconsistent-tag-signature
printf 'commit=%s\ntag_object=%s\nepoch=%s\n' \
  "$commit" "$tag_object" "$(git show -s --format=%ct "$commit")"
