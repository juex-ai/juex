# Artifact Storage

`artifact` owns safe, durable bytes under `<WorkDir>/.juex/artifacts`.

Its Store accepts logical paths relative to the artifact directory and returns
workspace-relative references containing the path, SHA-256, and stored byte
count. It centralizes:

- workspace-rooted path and symlink safety through `os.Root`;
- same-directory temporary writes and atomic replacement;
- idempotent content-addressed storage;
- integrity verification on read;
- bounded reads that reject oversized artifacts before loading them in full.

Callers retain format-specific decisions. The `read` tool detects and resizes
images, provider adapters encode verified media, and runtime context projection
chooses preview limits. Retention and garbage collection are intentionally
outside the Store contract.
