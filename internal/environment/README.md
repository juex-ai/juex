# Runtime Environment

> English | [中文](README.zh.md)

This package owns dotenv parsing, configured-name validation, immutable
environment snapshots, value-free provenance, child overlays, executable
lookup, and controlled process activation.

`internal/config` owns source discovery and precedence. Consumers receive a
snapshot explicitly; child-process launchers add local values before
Juex-owned runtime injection. Extension defaults never replace an existing
Agent key.

Diagnostics expose metadata or redacted values, never raw configured secrets.
Sandbox loader variables remain isolated from the wrapper process.
