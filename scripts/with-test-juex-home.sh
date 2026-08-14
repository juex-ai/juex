#!/usr/bin/env bash
set -euo pipefail

if (($# == 0)); then
  echo "usage: $0 command [args...]" >&2
  exit 64
fi

temp_parent="$(cd -- "${TMPDIR:-/tmp}" && pwd -P)"
test_root="$(mktemp -d "$temp_parent/juex-test-home.XXXXXX")"

cleanup() {
  rm -rf -- "$test_root"
}
trap cleanup EXIT

export JUEX_HOME="$test_root/.juex"
mkdir -p -- "$JUEX_HOME"

"$@"
