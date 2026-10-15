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
#   JUEX_INSTALL_LIBC=glibc
#   JUEX_INSTALL_PACKAGE_HOME=/custom/package/home
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
  if is_termux_android; then
    printf 'linux\n'
    return
  fi
  local raw="${JUEX_INSTALL_OS:-$(uname -s)}"
  case "$(printf '%s' "$raw" | tr '[:upper:]' '[:lower:]')" in
    darwin) printf 'darwin\n' ;;
    linux) printf 'linux\n' ;;
    mingw*|msys*|cygwin*|windows) printf 'windows\n' ;;
    *) die "unsupported operating system: ${raw}" ;;
  esac
}

is_termux_android() {
  [[ -n "${ANDROID_ROOT:-}" ]] ||
    [[ -n "${ANDROID_DATA:-}" ]] ||
    [[ "${PREFIX:-}" == *com.termux* ]]
}

detect_arch() {
  local raw="${JUEX_INSTALL_ARCH:-$(uname -m)}"
  case "$(printf '%s' "$raw" | tr '[:upper:]' '[:lower:]')" in
    x86_64|amd64) printf 'amd64\n' ;;
    arm64|aarch64) printf 'arm64\n' ;;
    armv7|armv7l|armv8l|armhf) printf 'armv7\n' ;;
    *) die "unsupported architecture: ${raw}" ;;
  esac
}

detect_linux_libc() {
  local raw="${JUEX_INSTALL_LIBC:-}"
  if [[ -n "$raw" ]]; then
    case "$(printf '%s' "$raw" | tr '[:upper:]' '[:lower:]')" in
      glibc|gnu) printf 'glibc\n' ;;
      musl) printf 'musl\n' ;;
      *) die "unsupported Linux libc override: ${raw}; expected glibc or musl" ;;
    esac
    return
  fi

  local version
  if command -v getconf >/dev/null 2>&1; then
    version=$(getconf GNU_LIBC_VERSION 2>/dev/null || true)
    if [[ "$version" == glibc* ]]; then
      printf 'glibc\n'
      return
    fi
  fi

  local ldd_output
  if command -v ldd >/dev/null 2>&1; then
    ldd_output=$(LC_ALL=C ldd --version 2>&1 || true)
    case "$(printf '%s' "$ldd_output" | tr '[:upper:]' '[:lower:]')" in
      *musl*) printf 'musl\n'; return ;;
      *glibc*|*"gnu libc"*|*"gnu c library"*) printf 'glibc\n'; return ;;
    esac
  fi

  local loader
  for loader in \
    /lib/ld-linux-aarch64.so.1 \
    /lib64/ld-linux-aarch64.so.1 \
    /lib/aarch64-linux-gnu/ld-linux-aarch64.so.1 \
    /usr/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1; do
    if [[ -e "$loader" ]]; then
      printf 'glibc\n'
      return
    fi
  done
  for loader in /lib/ld-musl-*.so.1 /usr/lib/ld-musl-*.so.1; do
    if [[ -e "$loader" ]]; then
      printf 'musl\n'
      return
    fi
  done
  printf 'unknown\n'
}

require_release_runtime() {
  local os_name="$1"
  local arch="$2"
  if [[ "$os_name" != "linux" || "$arch" != "arm64" ]]; then
    return
  fi
  local libc
  libc=$(detect_linux_libc)
  if [[ "$libc" != "glibc" ]]; then
    die "Linux arm64 release requires glibc because upstream ripgrep 15.1.0 publishes only a glibc asset; detected ${libc}. Use a source build with a compatible rg on PATH."
  fi
}

require_termux_runtime() {
  local arch="$1"
  case "$arch" in
    arm64|armv7) ;;
    *) die "Termux/Android release installation supports only arm64 and armv7; detected ${arch}" ;;
  esac
}

ensure_termux_ripgrep() {
  if command -v rg >/dev/null 2>&1; then
    return
  fi
  if ! command -v pkg >/dev/null 2>&1; then
    die "ripgrep is required on Termux but rg and pkg are unavailable; run pkg install -y ripgrep, then retry"
  fi
  if ! pkg install -y ripgrep; then
    die "failed to install ripgrep with pkg; run pkg install -y ripgrep, then retry"
  fi
  if ! command -v rg >/dev/null 2>&1; then
    die "pkg install -y ripgrep completed but did not make rg available on PATH"
  fi
}

warn_missing_sandbox_backend() {
  local os_name="$1"
  local termux_mode="$2"
  if [[ "$os_name" != "linux" ]] || command -v bwrap >/dev/null 2>&1; then
    return
  fi
  if [[ "$termux_mode" -eq 1 ]]; then
    printf 'warning: bubblewrap (bwrap) is not on PATH; the default Linux sandbox will fail closed. On rooted Termux, run pkg install -y root-repo && pkg install -y bubblewrap; otherwise explicitly set sandbox.enabled: false. Run juex diagnose after installation.\n' >&2
    return
  fi
  printf 'warning: bubblewrap (bwrap) is not on PATH; the default Linux sandbox will fail closed. Install the bubblewrap package or explicitly set sandbox.enabled: false. Run juex diagnose after installation.\n' >&2
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

is_transient_http_status() {
  case "$1" in
    408|429|500|502|503|504|522|524) return 0 ;;
    *) return 1 ;;
  esac
}

retry_after_delay() {
  local headers="$1"
  local value
  local epoch
  local now
  value=$(
    awk '
      {
        line = $0
        sub(/\r$/, "", line)
        if (tolower(substr(line, 1, 5)) == "http/") {
          value = ""
          next
        }
        if (tolower(substr(line, 1, 12)) == "retry-after:") {
          sub(/^[^:]*:[[:space:]]*/, "", line)
          sub(/[[:space:]]+$/, "", line)
          value = line
        }
      }
      END { print value }
    ' "$headers"
  )
  if [[ "$value" =~ ^[0-9]+$ ]]; then
    printf '%s\n' "$value"
    return
  fi
  if [[ -z "$value" ]]; then
    printf '2\n'
    return
  fi
  if epoch=$(LC_ALL=C date -d "$value" +%s 2>/dev/null); then
    :
  elif epoch=$(TZ=UTC LC_ALL=C date -u -D '%a, %d %b %Y %H:%M:%S GMT' -d "$value" +%s 2>/dev/null); then
    :
  elif epoch=$(TZ=UTC LC_ALL=C date -j -f '%a, %d %b %Y %H:%M:%S GMT' "$value" +%s 2>/dev/null); then
    :
  else
    printf '2\n'
    return
  fi
  now=$(date +%s)
  if [[ "$epoch" -gt "$now" ]]; then
    printf '%s\n' "$((epoch - now))"
  else
    printf '0\n'
  fi
}

download_with_curl() {
  local url="$1"
  local out="$2"
  local attempt=1
  local status
  local http_status
  local headers="${out}.headers.$$"
  local retry_delay
  while true; do
    http_status=""
    : >"$headers"
    if http_status=$(curl -fsSL -C - -D "$headers" -w '%{http_code}' "$url" -o "$out"); then
      rm -f "$headers"
      return 0
    else
      status=$?
    fi
    if [[ "$attempt" -ge 6 ]]; then
      rm -f "$headers"
      return "$status"
    fi
    if [[ "$status" -eq 22 ]] && ! is_transient_http_status "$http_status"; then
      rm -f "$headers"
      return "$status"
    fi
    retry_delay=2
    if [[ "$status" -eq 22 ]]; then
      retry_delay=$(retry_after_delay "$headers")
    fi
    rm -f "$headers"
    if [[ "$status" -eq 33 ]]; then
      rm -f "$out"
    fi
    sleep "$retry_delay"
    attempt=$((attempt + 1))
  done
}

download_with_wget() {
  local url="$1"
  local out="$2"
  local attempt=1
  local status
  local wget_help
  local wget_args=(-q -c)
  wget_help=$(wget --help 2>&1 || true)
  if [[ "$wget_help" == *"--tries="* ]]; then
    wget_args+=(-t 1)
  fi
  while true; do
    if wget "${wget_args[@]}" "$url" -O "$out"; then
      return 0
    else
      status=$?
    fi
    if [[ "$status" -eq 8 || "$attempt" -ge 6 ]]; then
      return "$status"
    fi
    sleep 2
    attempt=$((attempt + 1))
  done
}

download_file() {
  local url="$1"
  local out="$2"
  case "$url" in
    http://*|https://*)
      if command -v curl >/dev/null 2>&1; then
        download_with_curl "$url" "$out"
      elif command -v wget >/dev/null 2>&1; then
        download_with_wget "$url" "$out"
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
    sha256sum "$file" | awk '{sub(/^\\/, "", $1); print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{sub(/^\\/, "", $1); print $1}'
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

extract_archive() {
  local archive="$1"
  local out_dir="$2"
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
}

find_packaged_binary() {
  local out_dir="$1"
  local binary_name="$2"
  local extracted
  extracted=$(find "$out_dir" -type f -path "*/bin/$binary_name" -print | sed -n '1p')
  [[ -n "$extracted" ]] || die "packaged binary bin/${binary_name} not found in archive"
  printf '%s\n' "$extracted"
}

find_package_root() {
  local out_dir="$1"
  local manifest
  manifest=$(find "$out_dir" -type f -name juex-package.json -print | sed -n '1p')
  [[ -n "$manifest" ]] || die "release archive is missing expected juex-package.json package manifest"
  dirname "$manifest"
}

install_binary() {
  local source="$1"
  local target="$2"
  local tmp
  mkdir -p "$(dirname "$target")"
  tmp=$(mktemp "${target}.tmp.XXXXXX")
  if ! cp "$source" "$tmp"; then
    rm -f "$tmp"
    die "could not stage JueX binary for installation: $target"
  fi
  if ! chmod 0755 "$tmp"; then
    rm -f "$tmp"
    die "could not make staged JueX binary executable: $target"
  fi
  if ! mv -f "$tmp" "$target"; then
    rm -f "$tmp"
    die "could not atomically install JueX binary: $target"
  fi
}

replace_symlink() {
  local target="$1"
  local link="$2"
  local tmp="${link}.tmp.$$"
  rm -f "$tmp"
  ln -s "$target" "$tmp"
  if mv -Tf "$tmp" "$link" 2>/dev/null; then
    return
  fi
  if mv -hf "$tmp" "$link" 2>/dev/null; then
    return
  fi
  rm -f "$tmp"
  die "could not atomically replace symlink: $link"
}

install_managed_package() {
  local source_root="$1"
  local package_home="$2"
  local release_key="$3"
  local binary_name="$4"
  local install_target="$5"
  local rg_name="rg"
  if [[ "$binary_name" == *.exe ]]; then
    rg_name="rg.exe"
  fi

  [[ -f "$source_root/juex-package.json" ]] || die "managed release is missing juex-package.json"
  [[ -f "$source_root/bin/$binary_name" ]] || die "managed release is missing bin/$binary_name"
  [[ -f "$source_root/juex-path/$rg_name" ]] || die "managed release is missing juex-path/$rg_name"
  [[ -f "$source_root/juex-resources/licenses/ripgrep/LICENSE-MIT" ]] || die "managed release is missing ripgrep LICENSE-MIT"
  [[ -f "$source_root/juex-resources/licenses/ripgrep/UNLICENSE" ]] || die "managed release is missing ripgrep UNLICENSE"

  local releases_dir release_dir release_name stage generation
  releases_dir="${package_home%/}/releases"
  mkdir -p "$releases_dir" "$(dirname "$install_target")"
  stage=$(mktemp -d "${releases_dir}/.${release_key}.tmp.XXXXXX")
  generation="${stage##*.tmp.}"
  release_name="${release_key}-${generation}"
  release_dir="${releases_dir}/${release_name}"
  cp -R "$source_root/." "$stage/"
  chmod +x "$stage/bin/$binary_name" "$stage/juex-path/$rg_name"
  mv "$stage" "$release_dir"

  replace_symlink "releases/$release_name" "${package_home%/}/current"
  replace_symlink "${release_dir}/bin/$binary_name" "$install_target"
}

refresh_fleet_service() {
  local binary="$1"
  local installed
  if ! installed=$("$binary" fleet service-installed); then
    printf 'warning: JueX binary installed successfully, but could not check fleet service status.\n' >&2
    return 0
  fi
  case "$installed" in
    true)
      if "$binary" fleet install; then
        printf 'Refreshed existing JueX fleet service.\n'
        if ! "$binary" agent list --format json >/dev/null; then
          printf 'warning: refreshed JueX fleet service, but could not check running agent versions.\n' >&2
        fi
      else
        printf 'warning: JueX binary installed successfully, but failed to refresh existing fleet service.\n' >&2
      fi
      ;;
    false)
      if [[ "${INSTALL_FLEET_SERVICE:-0}" == "1" ]]; then
        if "$binary" fleet install; then
          printf 'Installed JueX fleet service by explicit request.\n'
          if ! "$binary" agent list --format json >/dev/null; then
            printf 'warning: installed JueX fleet service, but could not check running agent versions.\n' >&2
          fi
        else
          printf 'warning: JueX binary installed successfully, but failed to install the requested fleet service.\n' >&2
        fi
      else
        printf 'JueX fleet service is not installed; set INSTALL_FLEET_SERVICE=1 to install it during JueX installation.\n'
      fi
      ;;
    *)
      printf 'warning: unexpected fleet service state from %s: %s\n' "$binary" "$installed" >&2
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

  local resolved_version version_for_asset tag os_name arch archive checksums_url asset_url install_dir binary_name install_target package_home release_key termux_mode
  resolved_version=$(resolve_version "$requested_version")
  version_for_asset=$(asset_version "$resolved_version")
  tag=$(release_tag "$resolved_version")
  termux_mode=0
  if is_termux_android; then
    termux_mode=1
  fi
  os_name=$(detect_os)
  arch=$(detect_arch)
  if [[ "$termux_mode" -eq 1 ]]; then
    require_termux_runtime "$arch"
  else
    require_release_runtime "$os_name" "$arch"
  fi
  archive=$(archive_name "$version_for_asset" "$os_name" "$arch")
  asset_url=$(release_asset_url "$tag" "$archive")
  checksums_url=$(release_asset_url "$tag" "checksums.txt")
  install_dir="${bin_dir:-${prefix%/}/bin}"
  binary_name="juex"
  if [[ "$os_name" == "windows" ]]; then
    binary_name="juex.exe"
  fi
  install_target="${install_dir%/}/${binary_name}"
  package_home="${JUEX_INSTALL_PACKAGE_HOME:-$(dirname "$install_dir")/lib/juex}"
  release_key="${version_for_asset}-${os_name}-${arch}"

  cat <<EOF
JueX release install plan
version: ${version_for_asset}
release tag: ${tag}
platform: ${os_name}/${arch}
archive: ${archive}
asset url: ${asset_url}
checksum url: ${checksums_url}
install target: ${install_target}
EOF
  if [[ "$termux_mode" -eq 1 ]]; then
    cat <<EOF
install mode: Termux bare binary
ripgrep: system PATH (installed with pkg if missing)
sandbox: bubblewrap (bwrap) is required by the default policy; on rooted Termux run pkg install -y root-repo && pkg install -y bubblewrap, otherwise set sandbox.enabled: false
uninstall: rm -f ${install_target}
EOF
  else
    cat <<EOF
package home: ${package_home}
uninstall: rm -f ${install_target}; rm -rf ${package_home}
EOF
  fi

  if [[ "$dry_run" -eq 1 ]]; then
    return 0
  fi

  if [[ "$termux_mode" -eq 1 ]]; then
    ensure_termux_ripgrep
    mkdir -p "$install_dir"
  else
    mkdir -p "$install_dir" "$package_home"
  fi
  install_dir=$(cd "$install_dir" && pwd -P)
  if [[ "$termux_mode" -eq 0 ]]; then
    package_home=$(cd "$package_home" && pwd -P)
  fi
  install_target="${install_dir%/}/${binary_name}"

  local tmp archive_path checksums_path extract_dir package_root extracted
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
  extract_archive "$archive_path" "$extract_dir"
  if [[ "$termux_mode" -eq 1 ]]; then
    extracted=$(find_packaged_binary "$extract_dir" "$binary_name")
    install_binary "$extracted" "$install_target"
  else
    package_root=$(find_package_root "$extract_dir")
    install_managed_package "$package_root" "$package_home" "$release_key" "$binary_name" "$install_target"
  fi

  printf 'Installed juex to %s\n' "$install_target"
  warn_missing_sandbox_backend "$os_name" "$termux_mode"
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
