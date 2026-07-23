#!/usr/bin/env bash
# Prepare the pinned ripgrep payload embedded in a JueX release archive.

set -euo pipefail

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage:
  scripts/prepare-ripgrep.sh --target GOOS_GOARCH --juex-version VERSION --output DIR

The asset metadata is pinned in release/ripgrep-assets.tsv. Set
JUEX_RIPGREP_ASSET_MANIFEST, JUEX_RIPGREP_BASE_URL, or JUEX_RIPGREP_CACHE to
override inputs for deterministic tests or an offline build cache.
EOF
}

repo_root() {
  cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd
}

compute_sha256() {
  local file="$1"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$file" | awk '{sub(/^\\/, "", $1); print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$file" | awk '{sub(/^\\/, "", $1); print $1}'
  else
    die "sha256sum or shasum is required to verify ripgrep"
  fi
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
        die "curl or wget is required to download ripgrep"
      fi
      ;;
    file://*) cp "${url#file://}" "$out" ;;
    *) cp "$url" "$out" ;;
  esac
}

normalize_target() {
  case "$1" in
    darwin_amd64*) printf 'darwin_amd64\n' ;;
    darwin_arm64*) printf 'darwin_arm64\n' ;;
    linux_amd64*) printf 'linux_amd64\n' ;;
    linux_arm64*) printf 'linux_arm64\n' ;;
    linux_arm_7*|linux_armv7*) printf 'linux_armv7\n' ;;
    windows_amd64*) printf 'windows_amd64\n' ;;
    windows_arm64*) printf 'windows_arm64\n' ;;
    *) die "unsupported JueX release target: $1" ;;
  esac
}

target=""
juex_version=""
output=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --target)
      [[ $# -ge 2 ]] || die "--target requires a value"
      target="$2"
      shift 2
      ;;
    --juex-version)
      [[ $# -ge 2 ]] || die "--juex-version requires a value"
      juex_version="${2#v}"
      shift 2
      ;;
    --output)
      [[ $# -ge 2 ]] || die "--output requires a value"
      output="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *) die "unknown argument: $1" ;;
  esac
done

[[ -n "$target" ]] || die "--target is required"
[[ -n "$juex_version" ]] || die "--juex-version is required"
[[ -n "$output" ]] || die "--output is required"
[[ "$juex_version" =~ ^[A-Za-z0-9._+-]+$ ]] || die "--juex-version contains unsupported characters"
case "$output" in
  /|.|..) die "refusing unsafe package output directory: $output" ;;
esac

target=$(normalize_target "$target")
case "$target" in
  darwin_amd64) platform_os=darwin; platform_arch=amd64; binary_name=rg ;;
  darwin_arm64) platform_os=darwin; platform_arch=arm64; binary_name=rg ;;
  linux_amd64) platform_os=linux; platform_arch=amd64; binary_name=rg ;;
  linux_arm64) platform_os=linux; platform_arch=arm64; binary_name=rg ;;
  linux_armv7) platform_os=linux; platform_arch=arm; binary_name=rg ;;
  windows_amd64) platform_os=windows; platform_arch=amd64; binary_name=rg.exe ;;
  windows_arm64) platform_os=windows; platform_arch=arm64; binary_name=rg.exe ;;
esac

manifest="${JUEX_RIPGREP_ASSET_MANIFEST:-$(repo_root)/release/ripgrep-assets.tsv}"
[[ -f "$manifest" ]] || die "ripgrep asset manifest not found: $manifest"

rg_version=""
asset=""
asset_size=""
asset_sha=""
while IFS=$'\t' read -r row_target row_version row_asset row_size row_sha; do
  [[ -n "$row_target" && "${row_target#\#}" == "$row_target" ]] || continue
  if [[ "$row_target" == "$target" ]]; then
    rg_version="$row_version"
    asset="$row_asset"
    asset_size="$row_size"
    asset_sha="$row_sha"
    break
  fi
done < "$manifest"
[[ -n "$asset" ]] || die "ripgrep asset manifest has no entry for $target"

cache="${JUEX_RIPGREP_CACHE:-${TMPDIR:-/tmp}/juex-ripgrep-cache}"
mkdir -p "$cache"
asset_path="${cache%/}/$asset"
base_url="${JUEX_RIPGREP_BASE_URL:-https://github.com/BurntSushi/ripgrep/releases/download/${rg_version}}"

asset_valid=0
if [[ -f "$asset_path" ]]; then
  actual_size=$(wc -c < "$asset_path" | tr -d '[:space:]')
  actual_sha=$(compute_sha256 "$asset_path")
  if [[ "$actual_size" == "$asset_size" && "$actual_sha" == "$asset_sha" ]]; then
    asset_valid=1
  fi
fi
if [[ "$asset_valid" -ne 1 ]]; then
  download_tmp="${asset_path}.tmp.$$"
  rm -f "$download_tmp"
  download_file "${base_url%/}/$asset" "$download_tmp"
  actual_size=$(wc -c < "$download_tmp" | tr -d '[:space:]')
  [[ "$actual_size" == "$asset_size" ]] || die "ripgrep asset size mismatch for $asset: expected $asset_size, got $actual_size"
  actual_sha=$(compute_sha256 "$download_tmp")
  [[ "$actual_sha" == "$asset_sha" ]] || die "ripgrep asset checksum mismatch for $asset: expected $asset_sha, got $actual_sha"
  mv -f "$download_tmp" "$asset_path"
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/juex-ripgrep.XXXXXX")
trap 'rm -rf "$tmp"' EXIT
extract_dir="$tmp/extract"
stage="$tmp/package"
mkdir -p "$extract_dir" "$stage/juex-path" "$stage/juex-resources/licenses/ripgrep"

case "$asset" in
  *.tar.gz) tar -xzf "$asset_path" -C "$extract_dir" ;;
  *.zip)
    if command -v unzip >/dev/null 2>&1; then
      unzip -q "$asset_path" -d "$extract_dir"
    elif tar -tf "$asset_path" >/dev/null 2>&1; then
      tar -xf "$asset_path" -C "$extract_dir"
    else
      die "unzip or a zip-capable tar is required to extract $asset"
    fi
    ;;
  *) die "unsupported ripgrep archive format: $asset" ;;
esac

rg_source=$(find "$extract_dir" -type f -name "$binary_name" -print | sed -n '1p')
license_mit=$(find "$extract_dir" -type f -name LICENSE-MIT -print | sed -n '1p')
unlicense=$(find "$extract_dir" -type f -name UNLICENSE -print | sed -n '1p')
[[ -n "$rg_source" ]] || die "$binary_name not found in $asset"
[[ -n "$license_mit" && -n "$unlicense" ]] || die "ripgrep license files not found in $asset"

cp "$rg_source" "$stage/juex-path/$binary_name"
chmod +x "$stage/juex-path/$binary_name"
cp "$license_mit" "$stage/juex-resources/licenses/ripgrep/LICENSE-MIT"
cp "$unlicense" "$stage/juex-resources/licenses/ripgrep/UNLICENSE"
rg_sha=$(compute_sha256 "$stage/juex-path/$binary_name")

cat > "$stage/juex-package.json" <<EOF
{
  "schema_version": 1,
  "juex_version": "$juex_version",
  "platform": {"os": "$platform_os", "arch": "$platform_arch"},
  "ripgrep": {"version": "$rg_version", "path": "juex-path/$binary_name", "sha256": "$rg_sha"}
}
EOF

mkdir -p "$(dirname "$output")"
rm -rf "$output"
mv "$stage" "$output"
printf 'Prepared ripgrep %s for %s in %s\n' "$rg_version" "$target" "$output"
