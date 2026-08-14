#!/usr/bin/env bash
set -euo pipefail

if (($# == 0)); then
  echo "usage: $0 command [args...]" >&2
  exit 64
fi

temp_parent="${TMPDIR:-/tmp}"
test_root="$(mktemp -d "$temp_parent/juex-test-home.XXXXXX")"
test_root="$(cd -- "$test_root" && (pwd -W 2>/dev/null || pwd -P))"

cleanup() {
  rm -rf -- "$test_root"
}
trap cleanup EXIT

export JUEX_HOME="$test_root/.juex"
mkdir -p -- "$JUEX_HOME"

"$@"
