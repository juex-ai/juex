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
  if ! installed=$("$binary" fleet service-installed); then
    printf 'warning: JueX binary installed successfully, but could not check fleet service status.\n' >&2
    return 0
  fi
  case "$installed" in
    true)
      if "$binary" fleet install --restart-agents; then
        printf 'Refreshed existing JueX fleet service.\n'
        if ! "$binary" fleet status --format json >/dev/null; then
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
          if ! "$binary" fleet status --format json >/dev/null; then
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
# Install via write-then-rename rather than overwriting the target in place:
# on macOS, truncating an executable's bytes while a process still has it
# mapped (e.g. a running fleet daemon) corrupts code-signing/text-page state
# for that inode and the kernel SIGKILLs anything touching it, including the
# `version` check below. Rename swaps the directory entry to a fresh inode,
# leaving any already-running process on its old inode untouched.
INSTALL_TMP="${INSTALL_TARGET}.tmp.$$"
cp "$BUILD_TARGET" "$INSTALL_TMP"
chmod +x "$INSTALL_TMP"
mv -f "$INSTALL_TMP" "$INSTALL_TARGET"

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
