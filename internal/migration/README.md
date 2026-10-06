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
It captures configuration, `.env`, `AGENTS.md` and `.agents/AGENTS.md` from external
Workspaces, including exact bytes or recorded absence; it neither takes ownership
of them nor copies their arbitrary contents. Capturing configuration or opaque
extension files does not prove their behavior has been converted. A complete
migration still needs verified backups, service-owned import and behavior
acceptance before any user cutover.

`ReadExtension` captures an explicitly selected installation independently of
Fleet state. The caller proves the allow policy and winning root; the reader
never merges bundles or runs code. It captures the manifest and enabled resource
entrypoints with hashes and absence checks. Disabled categories are not probed.
Scripts and arbitrary auxiliary files remain part of the separate backup.
Symlink installations or selected resources need separate proven handling and
are rejected. This capture does not create target extension authority or prove
command, environment or private-state compatibility.

`ConvertStdioExtension` turns a selected v1 stdio declaration into the existing v2
manifest. Explicit executable, process cwd and Runtime WorkDir bindings preserve
separate source locations through a fixed argv launcher. Execution still owns
extension-private paths. Unsupported transports, Agent-wide defaults, unproved
resource absence and unresolved argument paths require separate conversion.
Conversion does not install dependencies, copy private state, connect MCP or
subscribe notifications; those require installation and behavior acceptance.

`internal/app/migration.ResolveConfig` resolves captured Home, Workspace, Agent
and explicit startup-file layers using the fixed source merge rules. Remote
imports require the exact source, declaring-file and startup-context cache
identity; conversion never fetches or republishes configuration. Reports omit
private provider values. The result describes disk configuration, not proven
inherited environment, authentication or target resource bindings. Unsupported
fields and missing or contradictory capture evidence stop resolution explicitly.

`ResolveModels` then applies explicitly supplied effective source environment and
captured Codex credentials. Unknown environment values are distinct from absent
or empty values; private profiles never enter JSON reports. It preserves fallback
selection and request-time identity templates without consulting the importing
process or refreshing tokens. The caller still has to prove process/auth capture
provenance and provider availability. Source output caps, target budget reservation
and Management account bindings must be reconciled before catalog publication;
successful source resolution alone is not a deployable target configuration.

`ConvertModels` builds a private publication plan from resolved profiles and
explicit reservations. Effective provider/model identities deduplicate only when
all private settings agree. Shared primary models require identical ordered,
direct fallback chains, including an explicit empty chain. Source request caps
and effective capabilities remain unchanged; an implicit endpoint must be resolved
before planning. The plan omits secrets from JSON and grants no authority to
overwrite existing catalog entries. Publication still needs Management validation,
private target-state comparison, tenant access and Agent bindings.

`ConvertAgentConfig` prepares a new Agent before resources are installed, not an
update patch. It maps resolved modules to existing capability groups and requires
explicit model, static instruction, Files, Shell, Calendar and collaboration
bindings. Source file/search switches do not match the target's grouped tools;
source Hooks and command Observables require target Shell even when the old Shell
module was off. Unresolved combinations are rejected. Old Supervisor
administration does not establish peer-message permission. Dynamic guidance
requires target Files; global guidance also requires the source user-resource
policy and a path on the selected Execution environment. Workspace/lifecycle,
Memory profile, skill selection and resource contents remain separate conversion
responsibilities.

Management's offline `ImportAgents` persists initial definitions and their source
ID mapping atomically. A matching retry recovers the same target identities;
names are not migration keys. It requires the expected fresh Fleet and rejects
recreation after purge. This receipt does not authorize subsequent service imports
or prove that source state, resources and lifecycle have been migrated.

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

Memory conversion uses those same Runtime identities and complete Commit
intervals for entry/fact provenance, review evidence, source cursors and no-store
ranges. It preserves terminal outcomes and knowledge revisions; old assignment
credentials never become current authority. Pending historical evidence remains
Memory-owned inert history, including assistant evidence the current Advanced
protocol does not collect. Source opt-outs survive configuration changes.
Unmapped ownership, history or range boundaries fail conversion. Original source
bytes retain legacy scheduling and recall statistics without making them live
jobs or new processing-success claims.

Calendar conversion reads captured v1 extension data with explicit same-owner
Agent bindings and a fixed capture time. It preserves rule clocks and next
instants, maps the source catch-up default to none, and uses the source ID when
its optional name is empty. Calendar creates new versions and authority epochs;
source data never supplies live delivery receipts. Pending deliveries, attachments,
sent history, scheduling errors and interval re-anchoring require separate proven
conversion and are rejected. A skipped one-shot with no future instant remains
completed. The old extension must not run alongside the new Calendar owner.
