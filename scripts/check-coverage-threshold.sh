#!/usr/bin/env bash
set -euo pipefail

threshold="${1:?usage: check-coverage-threshold.sh <minimum-percent>}"
profile="${2:-coverage.out}"

coverage="$(go tool cover -func="${profile}" | awk '/^total:/ { gsub(/%/, "", $3); print $3 }')"
if [[ -z "${coverage}" ]]; then
  echo "::error::Unable to determine coverage from ${profile}" >&2
  exit 1
fi

echo "Coverage: ${coverage}%"

if awk -v coverage="${coverage}" -v threshold="${threshold}" 'BEGIN { exit !(coverage < threshold) }'; then
  echo "::error::Coverage ${coverage}% is below ${threshold}% threshold" >&2
  exit 1
fi

echo "::notice::Coverage ${coverage}% meets ${threshold}% threshold"
