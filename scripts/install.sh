#!/usr/bin/env bash
# Install juex from a GitHub Release archive for the current machine.
#
# Usage:
#   scripts/install.sh
#   scripts/install.sh --version 0.0.1
#
# Environment:
#   PREFIX=/custom/prefix
#   JUEX_INSTALL_VERSION=0.0.1
#   JUEX_INSTALL_OS=linux
#   JUEX_INSTALL_ARCH=amd64
#   INSTALL_FLEET_SERVICE=1

set -euo pipefail

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Install juex from a GitHub Release.

Usage:
  scripts/install.sh [--version VERSION] [--prefix DIR] [--bin-dir DIR]

Options:
  --version VERSION  Release version to install, such as 0.0.1 or v0.0.1.
  --prefix DIR       Install prefix. Defaults to $PREFIX or ~/.local.
  --bin-dir DIR      Exact directory for the juex binary. Overrides --prefix.
  -h, --help         Show this help.
EOF
}

script_repo_root() {
  local source_path="${BASH_SOURCE[0]:-}"
  if [[ -n "$source_path" && -f "$source_path" ]]; then
    local script_dir
    script_dir=$(cd "$(dirname "$source_path")" && pwd)
    if [[ "${script_dir##*/}" == "scripts" && -f "${script_dir}/../CLI_CONFIG" ]]; then
      (cd "${script_dir}/.." && pwd)
    else
      printf '%s\n' "$script_dir"
    fi
  else
    pwd
  fi
}

read_cli_config_version() {
  local config="${JUEX_INSTALL_CLI_CONFIG:-$(script_repo_root)/CLI_CONFIG}"
  if [[ -f "$config" ]]; then
    awk -F= '/^VERSION=/{sub(/\r$/, "", $2); print $2; exit}' "$config"
  fi
}

release_tag() {
  local version="${1#v}"
  printf 'v%s\n' "$version"
}

asset_version() {
  printf '%s\n' "${1#v}"
}

resolve_latest_version() {
  if [[ -n "${JUEX_INSTALL_LATEST_VERSION:-}" ]]; then
    printf '%s\n' "$JUEX_INSTALL_LATEST_VERSION"
    return
  fi

  local repo_url="${JUEX_INSTALL_REPO_URL:-https://github.com/${JUEX_INSTALL_REPO:-juex-ai/juex}}"
  local effective_url
  if command -v curl >/dev/null 2>&1; then
    effective_url=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "${repo_url%/}/releases/latest") ||
      die "failed to resolve latest release from ${repo_url}"
  elif command -v wget >/dev/null 2>&1; then
    effective_url=$(wget -S --spider "${repo_url%/}/releases/latest" 2>&1 |
      awk 'tolower($1) == "location:" {sub(/\r$/, "", $2); url=$2} END {print url}') || true
    [[ -n "$effective_url" ]] || die "failed to resolve latest release from ${repo_url} using wget"
  else
    die "curl or wget is required to resolve the latest release"
  fi
  [[ "$effective_url" == *"/tag/"* ]] || die "could not resolve latest release from ${repo_url}"
  printf '%s\n' "${effective_url##*/}"
}

resolve_version() {
  local requested="${1:-}"
  if [[ -n "$requested" ]]; then
    if [[ "$requested" == "latest" ]]; then
      resolve_latest_version
      return
    fi
    printf '%s\n' "$requested"
    return
  fi

  local configured
  configured=$(read_cli_config_version || true)
  if [[ -n "$configured" ]]; then
    printf '%s\n' "$configured"
    return
  fi

  resolve_latest_version
}

detect_os() {
  local raw="${JUEX_INSTALL_OS:-$(uname -s)}"
  case "$(printf '%s' "$raw" | tr '[:upper:]' '[:lower:]')" in
    darwin) printf 'darwin\n' ;;
    linux) printf 'linux\n' ;;
    mingw*|msys*|cygwin*|windows) printf 'windows\n' ;;
    *) die "unsupported operating system: ${raw}" ;;
  esac
}

detect_arch() {
  local raw="${JUEX_INSTALL_ARCH:-$(uname -m)}"
  case "$(printf '%s' "$raw" | tr '[:upper:]' '[:lower:]')" in
    x86_64|amd64) printf 'amd64\n' ;;
    arm64|aarch64) printf 'arm64\n' ;;
    armv7|armv7l|armhf) printf 'armv7\n' ;;
    *) die "unsupported architecture: ${raw}" ;;
  esac
}

archive_name() {
  local version="$1"
  local os_name="$2"
  local arch="$3"
  local ext="tar.gz"
  if [[ "$os_name" == "windows" ]]; then
    ext="zip"
  fi
  printf 'juex_%s_%s_%s.%s\n' "$version" "$os_name" "$arch" "$ext"
}

release_asset_url() {
  local tag="$1"
  local asset="$2"
  if [[ -n "${JUEX_INSTALL_RELEASE_BASE_URL:-}" ]]; then
    printf '%s/%s\n' "${JUEX_INSTALL_RELEASE_BASE_URL%/}" "$asset"
    return
  fi

  local repo_url="${JUEX_INSTALL_REPO_URL:-https://github.com/${JUEX_INSTALL_REPO:-juex-ai/juex}}"
  printf '%s/releases/download/%s/%s\n' "${repo_url%/}" "$tag" "$asset"
}

download_file() {
  local url="$1"
  local out="$2"
  case "$url" in
    http://*|https://*)
      if command -v curl >/dev/null 2>&1; then
        curl -fsSL "$url" -o "$out"
      elif command -v wget >/dev/null 2>&1; then
        wget -q "$url" -O "$out"
      else
        die "curl or wget is required to download release assets"
      fi
      ;;
    file://*)
      cp "${url#file://}" "$out"
      ;;
    *)
      cp "$url" "$out"
      ;;
  esac
}

compute_sha256() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{print $1}'
  else
    die "sha256sum or shasum is required to verify release assets"
  fi
}

verify_checksum() {
  local archive="$1"
  local checksums="$2"
  local archive_base
  archive_base=$(basename "$archive")

  local expected
  expected=$(awk -v file="$archive_base" '{sub(/\r$/, "", $2)} ($2 == file || $2 == "*" file) {print $1; exit}' "$checksums")
  [[ -n "$expected" ]] || die "checksum entry not found for ${archive_base}"

  local actual
  actual=$(compute_sha256 "$archive")
  if [[ "$actual" != "$expected" ]]; then
    die "checksum mismatch for ${archive_base}: expected ${expected}, got ${actual}"
  fi
  printf 'checksum ok: %s\n' "$archive_base"
}

extract_binary() {
  local archive="$1"
  local out_dir="$2"
  local binary_name="$3"
  mkdir -p "$out_dir"

  case "$archive" in
    *.zip)
      command -v unzip >/dev/null 2>&1 || die "unzip is required to extract ${archive}"
      unzip -q "$archive" -d "$out_dir"
      ;;
    *.tar.gz)
      tar -xzf "$archive" -C "$out_dir"
      ;;
    *)
      die "unsupported archive format: ${archive}"
      ;;
  esac

  local extracted
  extracted=$(find "$out_dir" -type f -name "$binary_name" -print | sed -n '1p')
  [[ -n "$extracted" ]] || die "binary ${binary_name} not found in archive"
  printf '%s\n' "$extracted"
}

install_binary() {
  local source="$1"
  local target="$2"
  mkdir -p "$(dirname "$target")"
  rm -f "$target"
  cp "$source" "$target"
  chmod +x "$target"
}

refresh_fleet_service() {
  local binary="$1"
  local installed
  installed=$("$binary" fleet service-installed)
  case "$installed" in
    true)
      "$binary" fleet install
      printf 'Refreshed existing JueX fleet service.\n'
      "$binary" fleet status --format json >/dev/null
      ;;
    false)
      if [[ "${INSTALL_FLEET_SERVICE:-0}" == "1" ]]; then
        "$binary" fleet install
        printf 'Installed JueX fleet service by explicit request.\n'
        "$binary" fleet status --format json >/dev/null
      else
        printf 'JueX fleet service is not installed; set INSTALL_FLEET_SERVICE=1 to install it during JueX installation.\n'
      fi
      ;;
    *)
      die "unexpected fleet service state from ${binary}: ${installed}"
      ;;
  esac
}

main() {
  local dry_run=0
  local requested_version="${JUEX_INSTALL_VERSION:-}"
  local prefix="${PREFIX:-$HOME/.local}"
  local bin_dir="${JUEX_INSTALL_BIN_DIR:-}"

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version)
        [[ $# -ge 2 ]] || die "--version requires a value"
        requested_version="$2"
        shift 2
        ;;
      --prefix)
        [[ $# -ge 2 ]] || die "--prefix requires a value"
        prefix="$2"
        shift 2
        ;;
      --bin-dir)
        [[ $# -ge 2 ]] || die "--bin-dir requires a value"
        bin_dir="$2"
        shift 2
        ;;
      --dry-run)
        dry_run=1
        shift
        ;;
      -h|--help)
        usage
        return 0
        ;;
      *)
        die "unknown argument: $1"
        ;;
    esac
  done

  local resolved_version version_for_asset tag os_name arch archive checksums_url asset_url install_dir binary_name install_target
  resolved_version=$(resolve_version "$requested_version")
  version_for_asset=$(asset_version "$resolved_version")
  tag=$(release_tag "$resolved_version")
  os_name=$(detect_os)
  arch=$(detect_arch)
  archive=$(archive_name "$version_for_asset" "$os_name" "$arch")
  asset_url=$(release_asset_url "$tag" "$archive")
  checksums_url=$(release_asset_url "$tag" "checksums.txt")
  install_dir="${bin_dir:-${prefix%/}/bin}"
  binary_name="juex"
  if [[ "$os_name" == "windows" ]]; then
    binary_name="juex.exe"
  fi
  install_target="${install_dir%/}/${binary_name}"

  cat <<EOF
JueX release install plan
version: ${version_for_asset}
release tag: ${tag}
platform: ${os_name}/${arch}
archive: ${archive}
asset url: ${asset_url}
checksum url: ${checksums_url}
install target: ${install_target}
uninstall: rm -f ${install_target}
EOF

  if [[ "$dry_run" -eq 1 ]]; then
    return 0
  fi

  local tmp archive_path checksums_path extract_dir extracted
  tmp=$(mktemp -d)
  _juex_install_tmp="$tmp"
  trap 'rm -rf "$_juex_install_tmp"' EXIT
  archive_path="${tmp}/${archive}"
  checksums_path="${tmp}/checksums.txt"
  extract_dir="${tmp}/extract"

  printf '\nDownloading %s...\n' "$archive"
  download_file "$asset_url" "$archive_path"
  download_file "$checksums_url" "$checksums_path"
  verify_checksum "$archive_path" "$checksums_path"
  extracted=$(extract_binary "$archive_path" "$extract_dir" "$binary_name")
  install_binary "$extracted" "$install_target"

  printf 'Installed juex to %s\n' "$install_target"
  refresh_fleet_service "$install_target"
  if [[ ":$PATH:" != *":${install_dir}:"* ]]; then
    cat <<EOF

Note: ${install_dir} is not on your PATH.
Add this to your shell profile:

    export PATH="${install_dir}:\$PATH"
EOF
  fi
}

_juex_install_source="${BASH_SOURCE[0]:-}"
case "$_juex_install_source" in
  ""|stdin|/dev/stdin|/dev/fd/*)
    main "$@"
    ;;
  *)
    if [[ "$_juex_install_source" == "$0" || ! -f "$_juex_install_source" ]]; then
      main "$@"
    fi
    ;;
esac
