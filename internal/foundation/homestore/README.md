# Home Store

> English | [中文](README.zh.md)

This package owns portable filesystem mechanics for durable Juex state:
advisory locks, the `$JUEX_HOME/.locks` layout, atomic replacement, and
best-effort durability sync across supported filesystems.

Replacement retries only transient platform conflicts and never reports
success before the destination is published. Errors expose enough outcome
information for transactional callers to roll back only paths they own.

Callers with a checked `os.Root` use basename-only lock, publication, and sync
operations so directory renames cannot redirect I/O through the former path.
These operations sync file contents before rooted replacement. Directory sync
remains best effort; Windows rooted replacement does not expose the path API's
`MOVEFILE_WRITE_THROUGH` flag.

Identity, lifecycle, and multi-file transaction policy remain with callers
such as `agentstate`, `endpoint`, `fleet`, and `fleetservice`.
