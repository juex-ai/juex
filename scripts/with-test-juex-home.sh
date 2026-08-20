#!/usr/bin/env bash
set -euo pipefail

mode="deterministic"
if [[ "${1:-}" == "--live" ]]; then
  mode="live"
  shift
fi

if (($# == 0)); then
  echo "usage: $0 [--live] command [args...]" >&2
  exit 64
fi

original_home="${HOME:-}"
if [[ "${OS:-}" == "Windows_NT" && -n "${USERPROFILE:-}" ]]; then
  original_home="$USERPROFILE"
fi
original_workdir="$(pwd -W 2>/dev/null || pwd -P)"
repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && (pwd -W 2>/dev/null || pwd -P))"

temp_parent="${TMPDIR:-/tmp}"
test_root="$(mktemp -d "$temp_parent/juex-test-home.XXXXXX")"
test_root="$(cd -- "$test_root" && (pwd -W 2>/dev/null || pwd -P))"

cleanup() {
  rm -rf -- "$test_root"
}
trap cleanup EXIT

# Mise shims resolve trust and installed runtimes through HOME. Put the already
# selected runtime directories ahead of shims before HOME becomes temporary.
tool_path_prefix=""
if command -v mise >/dev/null 2>&1; then
  bootstrap_mise_state="$test_root/.bootstrap-mise-state"
  bootstrap_mise_cache="$test_root/.bootstrap-mise-cache"
  bootstrap_mise_trusted_config="$repo_root/mise.toml"
  if mise_bin_paths="$(MISE_STATE_DIR="$bootstrap_mise_state" MISE_CACHE_DIR="$bootstrap_mise_cache" MISE_TRUSTED_CONFIG_PATHS="$bootstrap_mise_trusted_config" mise bin-paths 2>/dev/null)"; then
    while IFS= read -r tool_dir; do
      if [[ -n "$tool_dir" ]]; then
        case "$tool_dir" in
          [[:alpha:]]:[\\/]*)
            if command -v cygpath >/dev/null 2>&1; then
              tool_dir="$(cygpath -u "$tool_dir")"
            else
              tool_dir="${tool_dir//\\//}"
            fi
            ;;
        esac
        tool_path_prefix="${tool_path_prefix:+$tool_path_prefix:}$tool_dir"
      fi
    done <<<"$mise_bin_paths"
  fi
fi
if [[ -n "$tool_path_prefix" ]]; then
  export PATH="$tool_path_prefix${PATH:+:$PATH}"
fi

# Resolve default tool caches before HOME moves so isolation does not turn every
# test run into a cold build or dependency download.
if command -v go >/dev/null 2>&1; then
  bootstrap_go_telemetry="$test_root/.bootstrap-go-telemetry"
  if [[ -z "${GOCACHE:-}" ]]; then
    export GOCACHE="$(TEST_TELEMETRY_DIR="$bootstrap_go_telemetry" go env GOCACHE)"
  fi
  if [[ -z "${GOMODCACHE:-}" ]]; then
    export GOMODCACHE="$(TEST_TELEMETRY_DIR="$bootstrap_go_telemetry" go env GOMODCACHE)"
  fi
fi
if [[ -z "${UV_CACHE_DIR:-}" ]] && command -v uv >/dev/null 2>&1; then
  export UV_CACHE_DIR="$(uv cache dir)"
fi

absolute_source_path() {
  local path="$1"
  case "$path" in
    "~") path="$original_home" ;;
    "~/"*) path="$original_home/${path#\~/}" ;;
    "~\\"*) path="$original_home/${path:2}"; path="${path//\\//}" ;;
  esac
  case "$path" in
    \\\\*)
      path="${path//\\//}"
      printf '%s\n' "$path"
      return
      ;;
    [[:alpha:]]:[\\/]*) path="${path//\\//}" ;;
    /*) ;;
    *) path="$original_workdir/$path" ;;
  esac

  local parent name
  parent="$(dirname -- "$path")"
  name="$(basename -- "$path")"
  if [[ -d "$parent" ]]; then
    parent="$(cd -- "$parent" && (pwd -W 2>/dev/null || pwd -P))"
  fi
  printf '%s/%s\n' "${parent%/}" "$name"
}

trim_space() {
  local value="$1"
  value="${value#"${value%%[![:space:]]*}"}"
  value="${value%"${value##*[![:space:]]}"}"
  printf '%s' "$value"
}

live_provider_config=""
live_codex_home=""
live_provider_config_is_default="false"
if [[ "$mode" == "live" ]]; then
  configured_provider_config="$(trim_space "${JUEX_PROVIDER_CONFIG:-}")"
  configured_codex_home="$(trim_space "${CODEX_HOME:-}")"
  if [[ -n "$configured_provider_config" ]]; then
    live_provider_config="$(absolute_source_path "$configured_provider_config")"
  elif [[ -n "$original_home" ]]; then
    live_provider_config="$(absolute_source_path "$original_home/.juex/juex.yaml")"
    live_provider_config_is_default="true"
  fi
  if [[ -n "$configured_codex_home" ]]; then
    live_codex_home="$(absolute_source_path "$configured_codex_home")"
  elif [[ -n "$original_home" ]]; then
    live_codex_home="$(absolute_source_path "$original_home/.codex")"
  fi
fi

export HOME="$test_root"
export USERPROFILE="$test_root"
export JUEX_HOME="$HOME/.juex"
export XDG_CONFIG_HOME="$HOME/.config"
export XDG_CACHE_HOME="$HOME/.cache"
export APPDATA="$HOME/AppData/Roaming"
export LOCALAPPDATA="$HOME/AppData/Local"
export TEST_TELEMETRY_DIR="$HOME/.config/go/telemetry"
export GIT_CONFIG_GLOBAL="$HOME/.gitconfig"
export MISE_STATE_DIR="$HOME/.local/state/mise"
export MISE_CACHE_DIR="$HOME/.cache/mise"
export MISE_CONFIG_DIR="$HOME/.config/mise"
if [[ "$mode" == "live" ]]; then
  if [[ -n "$live_provider_config" ]]; then
    export JUEX_PROVIDER_CONFIG="$live_provider_config"
  else
    unset JUEX_PROVIDER_CONFIG
  fi
  if [[ "$live_provider_config_is_default" == "true" ]]; then
    export JUEX_TEST_PROVIDER_CONFIG_DEFAULT="1"
  else
    unset JUEX_TEST_PROVIDER_CONFIG_DEFAULT
  fi
  if [[ -n "$live_codex_home" ]]; then
    export CODEX_HOME="$live_codex_home"
  else
    unset CODEX_HOME
  fi
else
  unset JUEX_PROVIDER_CONFIG
  unset JUEX_TEST_PROVIDER_CONFIG_DEFAULT
  export CODEX_HOME="$HOME/.codex"
fi
mkdir -p -- "$JUEX_HOME"

"$@"
