# Release Notes

## Unreleased

### Features

- Published and local-install packages now bundle a checksum-pinned ripgrep
  executable. The builtin `grep` tool runs it as a cancellable, bounded child
  process, and `juex doctor` reports the selected ripgrep source and path.
- `juex run`, `juex repl`, and `juex listen` accept `--ephemeral` for
  isolated temporary agent state. State is removed on exit; `--keep` retains it
  and prints the path to stderr.
- Read-only session, bundle, doctor, version, and schema operations no longer
  create workspace identities, registry entries, or global Git excludes.
- Fleet CLI and API log requests now explain when an agent has no
  fleet-owned log instead of exposing a raw file-open error.
