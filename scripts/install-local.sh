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

require_local_ripgrep_runtime() {
  local os_name="$1"
  local arch="$2"
  if [[ "$os_name" != "linux" || "$arch" != "arm64" ]]; then
    return
  fi
  local libc
  libc=$(detect_linux_libc)
  if [[ "$libc" != "glibc" ]]; then
    die "Linux arm64 local install requires glibc because upstream ripgrep 15.1.0 publishes only a glibc asset; detected ${libc}. Use an unpackaged source build with a compatible rg on PATH or JUEX_RG."
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
require_local_ripgrep_runtime "$GOOS" "$GOARCH"
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
STAGE=$(mktemp -d "${RELEASES_DIR}/.${RELEASE_KEY}.tmp.XXXXXX")
GENERATION="${STAGE##*.tmp.}"
RELEASE_NAME="${RELEASE_KEY}-${GENERATION}"
RELEASE_DIR="${RELEASES_DIR}/${RELEASE_NAME}"
cp -R "$PACKAGE_ROOT/." "$STAGE/"
chmod +x "$STAGE/bin/juex" "$STAGE/juex-path/rg"
mv "$STAGE" "$RELEASE_DIR"

replace_symlink "releases/$RELEASE_NAME" "${PACKAGE_HOME}/current"

# Point at the immutable generation so executable-path discovery cannot follow
# a later current switch away from the package that owns the running process.
replace_symlink "${RELEASE_DIR}/bin/juex" "$INSTALL_TARGET"

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
