# Release Notes

## Unreleased

### Features

- `juex run`, `juex repl`, and `juex serve` now accept `--ephemeral` for
  isolated temporary agent state. State is removed on exit; `--keep` retains it
  and prints the path to stderr.
- Read-only session, bundle, doctor, version, and schema operations no longer
  create workspace identities, registry entries, or global Git excludes.
- Fleet CLI and API log requests now explain when an agent has no
  fleet-owned log instead of exposing a raw file-open error.

### Compatibility

- `juex serve` no longer opens `http://127.0.0.1:8080` by default. It
  publishes only the canonical local agent endpoint unless `--addr` is passed.
  Scripts that call the agent JSON/SSE API over TCP must now pass an explicit
  address, for example `juex serve --addr 127.0.0.1:8080`.
