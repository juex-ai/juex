# Offline migration

> English | [中文](README.zh.md)

`legacy` reads the fixed `281889e5` Fleet, Agent, Thread and Memory formats
without opening their recovery-capable stores. It retains original source
bytes, chronological facts, exact current Generation context, terminal Inputs,
Memory knowledge/receipts/deletion constraints and private module/extension
files. It has no database, service, model or tool access.

A successful read checks framing, cursors, identities, usage and source-file
stability; it does not prove that a live Fleet is quiescent. Missing empty input
queues are recorded as absent and checked again before capture completes.
Incomplete batches, unknown facts, unsettled inputs, ambiguous Thread names,
inconsistent projections and unfinished Memory/configuration transactions are
rejected without repairing the source. Archive/restore metadata remains the
authority for Worker retention even when the last Turn failed. Memory no-store
ranges retain their original scope, including ranges spanning Generations.

Agent capture includes referenced media/spool bytes and verifies their hashes.
Fleet capture requires the source user's default Home explicitly, preserves
configuration layers and import-cache bytes, and records absent configuration.
It reads only configuration from external Workspaces; it neither takes ownership
of them nor copies their arbitrary contents. Capturing configuration or opaque
extension files does not prove their behavior has been converted. A complete
migration still needs verified backups, service-owned import and behavior
acceptance before any user cutover.

Pure message and terminal-input conversion lives in `internal/app/migration`,
where typed service contracts may meet the fixed source reader. Message identities
are scoped to the target Agent and Thread. Text spool references become verified
full text; image references require matching private Execution Artifact receipts.
Generated compaction references are rewritten only after exact reconstruction of
the source suffix, preserving the model-authored body. A settled source input is
not assumed successful: its matching recorded terminal event distinguishes
completion, cancellation and failure. These converters perform no I/O or replay.

Runtime conversion preserves inert source Commit/Input records alongside canonical
messages and an explicit continuation checkpoint. Only the source provider-visible
messages enter that checkpoint; policy-only and rejected content remains readable
history. Identity maps and closed Commit event intervals preserve later evidence
range mapping. Conflicting message copies or unresolved references fail conversion.
Verified source Memory assignment files retain Worker purpose and an inert review
relationship without copying credentials. Several historical Workers may reference
one business review; conversion never invents jobs or combines their outcomes.
Other application job relationships and model origins require explicit verified
bindings. Current compaction metadata containing an image without a usable provider
path is rejected.
