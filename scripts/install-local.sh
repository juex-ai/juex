#!/usr/bin/env bash
# Build juex for the current machine (output to dist/) and install the
# binary to ~/.local/bin/juex.
#
# Usage:
#   scripts/install-local.sh
#   PREFIX=$HOME/somewhere-else scripts/install-local.sh
#
# After install the script reminds you to add the prefix to PATH if missing.

set -euo pipefail

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
