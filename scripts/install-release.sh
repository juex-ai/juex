#!/usr/bin/env bash
# Compatibility wrapper for the root release installer.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
exec "${repo_root}/install.sh" "$@"
