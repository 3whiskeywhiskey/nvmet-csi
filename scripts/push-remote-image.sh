#!/usr/bin/env bash
set -euo pipefail

archive="${1:?usage: push-remote-image.sh /nix/store/...tar.gz}"
remote="${NIX_BUILDER_REMOTE:?NIX_BUILDER_REMOTE must be set}"
registry="${REGISTRY:?REGISTRY must be set}"
registry_user="${REGISTRY_USER:?REGISTRY_USER must be set}"
registry_token="${REGISTRY_TOKEN:?REGISTRY_TOKEN must be set}"
image="${IMAGE:?IMAGE must be set}"
tags="${IMAGE_TAGS:?IMAGE_TAGS must be set}"
remote_tmp_parent="${NIX_REMOTE_SKOPEO_TMPDIR:-/nix/var/nix/builds/skopeo-tmp}"

case "$archive" in
  /nix/store/*) ;;
  *)
    echo "archive must be a Nix store path on the remote builder: $archive" >&2
    exit 1
    ;;
esac

shell_quote() {
  local value="${1}"
  printf "'%s'" "${value//\'/\'\\\'\'}"
}

login_cmd="nix --extra-experimental-features 'nix-command flakes' shell nixpkgs#skopeo -c skopeo login $(shell_quote "$registry") --username $(shell_quote "$registry_user") --password-stdin"
printf '%s\n' "$registry_token" | ssh "$remote" "$login_cmd"

for tag in $tags; do
  destination="${image}:${tag}"
  echo "Pushing $destination from $remote"
  ssh "$remote" bash -s -- "$archive" "$destination" "$remote_tmp_parent" <<'REMOTE'
set -euo pipefail

archive="$1"
destination="$2"
tmp_parent="$3"

mkdir -p "$tmp_parent"
tmpdir="$(mktemp -d "$tmp_parent/push.XXXXXX")"
cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

TMPDIR="$tmpdir" nix --extra-experimental-features 'nix-command flakes' shell nixpkgs#skopeo -c skopeo --tmpdir "$tmpdir" --insecure-policy copy \
  "docker-archive:$archive" \
  "docker://$destination"
REMOTE
done
