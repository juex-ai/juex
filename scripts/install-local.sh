#!/usr/bin/env bash
# Build juex for the current machine (output to dist/) and install the
# binary to ~/.local/bin/juex.
#
# Usage:
#   scripts/install-local.sh
#   PREFIX=$HOME/somewhere-else scripts/install-local.sh
#   INSTALL_FLEET_SERVICE=1 scripts/install-local.sh
#
# After install the script reminds you to add the prefix to PATH if missing.

set -euo pipefail

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
      printf 'error: unexpected fleet service state from %s: %s\n' "$binary" "$installed" >&2
      return 1
      ;;
  esac
}

cd "$(dirname "$0")/.."

PREFIX=${PREFIX:-"$HOME/.local"}
INSTALL_DIR="${PREFIX}/bin"
INSTALL_TARGET="${INSTALL_DIR}/juex"

CLI_CONFIG_VERSION=$(awk -F= '/^VERSION=/{print $2}' CLI_CONFIG)
VERSION=${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo "${CLI_CONFIG_VERSION}-dev")}
COMMIT=${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)}
BUILD_TIME=${BUILD_TIME:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}

LDFLAGS="-s -w \
  -X github.com/juex-ai/juex/internal/version.Version=${VERSION} \
  -X github.com/juex-ai/juex/internal/version.Commit=${COMMIT} \
  -X github.com/juex-ai/juex/internal/version.BuildTime=${BUILD_TIME}"

GOOS=$(go env GOOS)
GOARCH=$(go env GOARCH)

DIST=dist
BUILD_TARGET="${DIST}/juex"
mkdir -p "$DIST"

echo "Building juex ${VERSION} for ${GOOS}/${GOARCH} → ${BUILD_TARGET} ..."
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$BUILD_TARGET" ./cmd/juex

echo "Installing → ${INSTALL_TARGET}"
mkdir -p "$INSTALL_DIR"
cp "$BUILD_TARGET" "$INSTALL_TARGET"
chmod +x "$INSTALL_TARGET"

"$INSTALL_TARGET" version
refresh_fleet_service "$INSTALL_TARGET"

case ":${PATH}:" in
  *":${INSTALL_DIR}:"*)
    ;;
  *)
    cat <<EOF

Note: ${INSTALL_DIR} is not on your PATH.
Add the following to your shell profile (e.g. ~/.zshrc or ~/.bashrc):

    export PATH="${INSTALL_DIR}:\$PATH"

Then restart the shell or 'source' the file.
EOF
    ;;
esac
