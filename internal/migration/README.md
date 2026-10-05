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
