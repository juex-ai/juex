#!/usr/bin/env bash
# Ensure ripgrep is resolvable for local test runs, and print the directory to
# prepend to PATH. This mirrors the CI provisioning step so a fresh checkout can
# run `make test` / `make race` / `make integration` without a system-installed
# ripgrep:
#
#   export PATH="$(scripts/ensure-ripgrep.sh):$PATH"
#
# We provision via PATH rather than JUEX_RG on purpose. The grep tool's resolver
# (internal/tools/ripgrep_resolver.go) treats JUEX_RG as an override that
# short-circuits every other source, so exporting it for the whole `go test`
# process would also override the resolver's own unit tests that read the
# ambient environment. Adding the pinned ripgrep to PATH keeps JUEX_RG unset and
# matches exactly what .github/workflows/ci.yml does.
#
# The grep tool resolves ripgrep from three sources: a JUEX_RG override, a
# release-package layout beside the running binary, then the system PATH. The
# package source never matches under `go test` (the test binary lives in the Go
# build cache, not a release package), so local runs rely on PATH.
#
# Make expands this script before starting Go tests. Keep its bootstrap `go env`
# telemetry writes disposable without changing the caller's test environment.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# type -P reports only a real executable on PATH, ignoring shell functions and
# aliases -- matching what Go's exec.LookPath can actually invoke.
if system_rg="$(type -P rg 2>/dev/null)" && [ -n "$system_rg" ]; then
  dirname "$system_rg"
  exit 0
fi

cache_dir="$repo_root/.tmp/dev-ripgrep"
go_telemetry_dir="$cache_dir/juex-ripgrep-telemetry.$$"
cleanup() {
  if command -v rm >/dev/null 2>&1; then
    rm -rf -- "$go_telemetry_dir"
  fi
}
trap cleanup EXIT

go_env() {
  TEST_TELEMETRY_DIR="$go_telemetry_dir" go env "$@"
}

os="$(go_env GOOS)"
binary_name="rg"
if [ "$os" = "windows" ]; then
  binary_name="rg.exe"
fi
rg_bin="$cache_dir/juex-path/$binary_name"
if [ ! -x "$rg_bin" ]; then
  arch="$(go_env GOARCH)"
  # GOARCH=arm names the 32-bit ARM family; prepare-ripgrep.sh keys the pinned
  # asset by GOARM level (e.g. linux_armv7), so fold GOARM in before the call.
  if [ "$arch" = "arm" ]; then
    arch="armv$(go_env GOARM)"
  fi
  # prepare-ripgrep.sh downloads and sha256-verifies the pinned release asset.
  # Its progress goes to stderr so stdout stays a clean path for callers.
  "$repo_root/scripts/prepare-ripgrep.sh" \
    --target "${os}_${arch}" \
    --juex-version dev \
    --output "$cache_dir" >&2
fi
dirname "$rg_bin"
