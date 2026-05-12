#!/usr/bin/env bash
# Build juex for every supported (GOOS, GOARCH) combination.
#
# Output:
#   dist/juex_<version>_<os>_<arch>/juex[.exe]  raw binary
#   dist/juex_<version>_<os>_<arch>.tar.gz      binary-only archive (zip on windows)
#   dist/checksums.txt                          sha256 of each archive
#
# Usage:
#   scripts/build.sh             # uses CLI_CONFIG VERSION + git describe
#   VERSION=v0.0.1 scripts/build.sh
#
# This is the dependency-free path (only requires `go`); for the canonical
# release build run `make snapshot` or push a `v*` tag to trigger goreleaser.

set -euo pipefail

cd "$(dirname "$0")/.."

CLI_CONFIG_VERSION=$(awk -F= '/^VERSION=/{print $2}' CLI_CONFIG)
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "${CLI_CONFIG_VERSION}-dev")}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
BUILD_TIME=${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}

LDFLAGS="-s -w \
  -X github.com/juex-ai/juex/internal/version.Version=${VERSION} \
  -X github.com/juex-ai/juex/internal/version.Commit=${COMMIT} \
  -X github.com/juex-ai/juex/internal/version.BuildTime=${BUILD_TIME}"

PLATFORMS=(
  "darwin amd64"
  "darwin arm64"
  "linux amd64"
  "linux arm64"
  "linux arm"
  "windows amd64"
  "windows arm64"
)

# GOARM applies only when GOARCH=arm. v7 covers Pi 2+, modern 32-bit
# Android, BeagleBone, etc. (matches goreleaser config).
ARM_VERSION=7

DIST=dist
rm -rf "$DIST"
mkdir -p "$DIST"

echo "Building juex ${VERSION} (commit ${COMMIT}) for ${#PLATFORMS[@]} targets..."

for entry in "${PLATFORMS[@]}"; do
  read -r GOOS GOARCH <<<"$entry"
  ext=""
  if [ "$GOOS" = "windows" ]; then ext=".exe"; fi
  arch_label="$GOARCH"
  if [ "$GOARCH" = "arm" ]; then arch_label="armv${ARM_VERSION}"; fi
  base="juex_${VERSION}_${GOOS}_${arch_label}"
  bin="${DIST}/${base}/juex${ext}"
  mkdir -p "${DIST}/${base}"

  echo "  → ${GOOS}/${arch_label}"
  if [ "$GOARCH" = "arm" ]; then
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" GOARM="$ARM_VERSION" \
      go build -trimpath -ldflags "$LDFLAGS" -o "$bin" ./cmd/juex
  else
    CGO_ENABLED=0 GOOS="$GOOS" GOARCH="$GOARCH" \
      go build -trimpath -ldflags "$LDFLAGS" -o "$bin" ./cmd/juex
  fi

  if [ "$GOOS" = "windows" ]; then
    (cd "$DIST" && zip -qr "${base}.zip" "${base}")
  else
    tar -czf "${DIST}/${base}.tar.gz" -C "$DIST" "${base}"
  fi
done

echo "Computing checksums..."
(
  cd "$DIST"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum juex_*.{tar.gz,zip} 2>/dev/null > checksums.txt || true
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 juex_*.tar.gz juex_*.zip 2>/dev/null > checksums.txt || true
  fi
)

echo
echo "Done. Artifacts in ${DIST}/:"
ls -1 "$DIST"
