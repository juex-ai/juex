# Release Notes

## Unreleased

### Features

- Published and local-install packages now bundle a checksum-pinned ripgrep
  executable. The builtin `grep` tool runs it as a cancellable, bounded child
  process, and `juex doctor` reports the selected ripgrep source and path.
- `juex run`, `juex repl`, and `juex serve` now accept `--ephemeral` for
  isolated temporary agent state. State is removed on exit; `--keep` retains it
  and prints the path to stderr.
- Read-only session, bundle, doctor, version, and schema operations no longer
  create workspace identities, registry entries, or global Git excludes.
- Fleet CLI and API log requests now explain when an agent has no
  fleet-owned log instead of exposing a raw file-open error.

### Compatibility

- Release installers now use versioned package directories under
  `<prefix>/lib/juex`; each install creates a new immutable generation and
  preserves older generations used by running processes. Existing binary-only
  archives remain installable.
  Termux/Android release installation is rejected until upstream provides a
  compatible pinned ripgrep asset. The Linux arm64 release requires glibc
  because that is the only upstream arm64 asset; release and local
  managed-package installers reject musl or an unverified libc before using
  it. Windows switches `current.txt` only after the new executable copy
  succeeds. Unpackaged source builds require `rg` on `PATH` or an explicit
  `JUEX_RG` path.
- `juex serve` no longer opens `http://127.0.0.1:8080` by default. It
  publishes only the canonical local agent endpoint unless `--addr` is passed.
  Scripts that call the agent JSON/SSE API over TCP must now pass an explicit
  address, for example `juex serve --addr 127.0.0.1:8080`.
