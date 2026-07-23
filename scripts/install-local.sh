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

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
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

if [[ -n "${ANDROID_ROOT:-}" || -n "${ANDROID_DATA:-}" || "${PREFIX:-}" == *com.termux* ]]; then
  printf 'error: Termux/Android is not supported because this build has no compatible bundled ripgrep asset\n' >&2
  exit 1
fi

PREFIX=${PREFIX:-"$HOME/.local"}
INSTALL_DIR="${PREFIX}/bin"
INSTALL_TARGET="${INSTALL_DIR}/juex"
PACKAGE_HOME="${JUEX_INSTALL_PACKAGE_HOME:-${PREFIX}/lib/juex}"

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
TARGET_ARCH="$GOARCH"
if [[ "$GOARCH" == "arm" ]]; then
  TARGET_ARCH="armv$(go env GOARM)"
fi

DIST=dist
PACKAGE_ROOT="${DIST}/juex-package"
BUILD_TARGET="${PACKAGE_ROOT}/bin/juex"

scripts/prepare-ripgrep.sh \
  --target "${GOOS}_${TARGET_ARCH}" \
  --juex-version "$VERSION" \
  --output "$PACKAGE_ROOT"
mkdir -p "$(dirname "$BUILD_TARGET")"

echo "Building juex ${VERSION} for ${GOOS}/${GOARCH} → ${BUILD_TARGET} ..."
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o "$BUILD_TARGET" ./cmd/juex

echo "Installing managed package → ${PACKAGE_HOME}"
VERSION_KEY="${VERSION#v}"
VERSION_KEY="${VERSION_KEY//\//-}"
VERSION_KEY="${VERSION_KEY//\\/-}"
VERSION_KEY="${VERSION_KEY//:/-}"
RELEASE_KEY="${VERSION_KEY}-${GOOS}-${GOARCH}"
mkdir -p "${PACKAGE_HOME}/releases" "$INSTALL_DIR"
INSTALL_DIR=$(cd "$INSTALL_DIR" && pwd -P)
PACKAGE_HOME=$(cd "$PACKAGE_HOME" && pwd -P)
INSTALL_TARGET="${INSTALL_DIR}/juex"
RELEASES_DIR="${PACKAGE_HOME}/releases"
RELEASE_DIR="${RELEASES_DIR}/${RELEASE_KEY}"
STAGE="${RELEASES_DIR}/.${RELEASE_KEY}.tmp.$$"
rm -rf "$STAGE"
mkdir -p "$STAGE"
cp -R "$PACKAGE_ROOT/." "$STAGE/"
chmod +x "$STAGE/bin/juex" "$STAGE/juex-path/rg"
rm -rf "$RELEASE_DIR"
mv "$STAGE" "$RELEASE_DIR"

replace_symlink "releases/$RELEASE_KEY" "${PACKAGE_HOME}/current"

# Swap a new symlink into place so a running daemon keeps its current inode.
replace_symlink "${PACKAGE_HOME}/current/bin/juex" "$INSTALL_TARGET"

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
