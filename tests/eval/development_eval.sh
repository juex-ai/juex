#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"

if ! command -v uv >/dev/null 2>&1; then
  echo "Missing uv. Install uv or run through the project toolchain before using development_eval.sh." >&2
  exit 2
fi

cd "$repo_root"
exec uv run --quiet --project "$repo_root" python -m tests.eval.juex_eval development "$@"
