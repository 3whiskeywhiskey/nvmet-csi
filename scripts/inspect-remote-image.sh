#!/usr/bin/env bash
set -euo pipefail

archive="${1:?usage: inspect-remote-image.sh /nix/store/...tar.gz}"
remote="${NIX_BUILDER_REMOTE:?NIX_BUILDER_REMOTE must be set}"

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

ssh "$remote" "nix --extra-experimental-features 'nix-command flakes' shell nixpkgs#skopeo -c skopeo inspect docker-archive:$(shell_quote "$archive")"
