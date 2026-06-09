#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/.." && pwd)"
cd "$repo_root"

if ! command -v uv >/dev/null 2>&1; then
  echo "Missing uv. Install uv or run through the project toolchain before using provider_model_smoke.sh." >&2
  exit 2
fi

run_py() {
  uv run --quiet --project "$repo_root" python "$@"
}

has_explicit_scope=0
if [[ -n "${JUEX_PROVIDER_SMOKE_ONLY:-}" ]] || [[ "${JUEX_PROVIDER_SMOKE_ALL_MODELS:-}" =~ ^(1|true|TRUE|yes|YES|on|ON)$ ]] || [[ "${JUEX_PROVIDER_SMOKE_ALL_CONFIG_MODELS:-}" =~ ^(1|true|TRUE|yes|YES|on|ON)$ ]]; then
  has_explicit_scope=1
fi
for arg in "$@"; do
  case "$arg" in
    --only|--only=*|--all-models|--all-config-models|-h|--help)
      has_explicit_scope=1
      ;;
  esac
done

if [[ "$has_explicit_scope" == 1 ]]; then
  run_py scripts/evalhelper.py provider-smoke "$@"
  exit $?
fi

selected_model="$(run_py scripts/live_model_rotation.py select --section provider_smoke_models)"
echo "rotated provider smoke model: ${selected_model}"

if run_py scripts/evalhelper.py provider-smoke --only "$selected_model" "$@"; then
  run_py scripts/live_model_rotation.py mark-success --section provider_smoke_models --model "$selected_model"
else
  exit $?
fi
