# Runtime Environment

This package owns deterministic dotenv parsing, portable configured-name
validation, immutable runtime snapshots, value-free provenance metadata,
ordered child overlays, executable lookup against the snapshot, and controlled
process activation.

`internal/config` owns source discovery and precedence. It builds one snapshot
from user YAML, `<WorkDir>/.env`, workspace YAML, explicit YAML, and the
inherited launch environment. Generic config loading and validation never
activates it. Runtime-bearing CLI commands activate one workspace snapshot and
restore test process state on exit; a second simultaneous activation fails.

`internal/app` may derive a second immutable Agent snapshot by applying
selected Extension declarations as low-priority defaults. Existing Agent keys,
including empty values, shadow defaults; equal declarations deduplicate and
different unshadowed declarations conflict. Extension defaults are marked
child-only and never participate in `Snapshot.Activate`.

Consumers receive the Agent snapshot explicitly. MCP, Observable, hook, shell,
and grep launchers add child-local values before Juex-owned runtime injection.
Sandbox helpers are resolved from the separately captured launch environment;
loader-injection variables are withheld from the wrapper and restored only for
the target inside the sandbox boundary.

Diagnostics use `ConfiguredMetadata`, value-free default declaration metadata,
`RedactConfiguredValues`, or `RedactConfiguredJSON`. They never enumerate raw
configured values. Every non-empty Extension declaration participates in
redaction, including shadowed, deduplicated, and conflicting values. Empty
values remain meaningful in the runtime snapshot but are skipped as redaction
patterns.
