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

`EncodeCapture` writes a private, fixed-version Fleet capture; `DecodeCapture`
requires its independently recorded SHA-256 and never reopens source paths.
Original bytes are deduplicated without merging ownership or absence records.
Thread coordinates, continuation and Memory projections are rebuilt and checked
against those bytes. Reporting JSON is not a capture format. The capture contains
private history and configuration; startup/auth evidence, selected extension
installations and destination bindings still need the operator's enclosing bundle.

App's `WriteBundle` freezes that capture, private configuration/auth/resource
evidence, explicit target policies and destination identity in three files.
`LoadBundle` requires an independently retained manifest digest and destination;
it rejects changed, missing, non-private or symlink payloads. File bytes and
unknown/absent/empty environment values survive round trips. Pure `Prepare`
resolves the model catalog and initial Agent/resource policies without reopening
source paths or allocating target UUIDs. This does not prove source shutdown,
authorize database writes, install resources or activate Agents; owner imports
and actual behavior acceptance remain necessary.
Enabled source Agents require an explicit target on-demand activation choice.
Their old process autostart flags remain in the capture; shared Runtime does not
reproduce per-Agent login jobs. Disabled source Agents need separate conversion.

`SourceGuard` opens only the fixed source version's existing writer locks and
holds them through import. Missing/busy locks, replaced directories or a changed
Agent registry stop the operation; the guard never creates or repairs source
files. Capture borrows its opened root. `VerifySource` compares the complete
capture with the frozen bundle without refreshing its identity on mismatch.
These locks do not cover the old Fleet HTTP recovery handler or external editors:
the operator must independently stop source services and their children, disable
autostart, and retain verified backups before capture and cutover.

The platform-only `juex-migrate apply` helper reads an existing private Host
deployment and requires the operator's inherited exclusive maintenance descriptor.
Existing storage directories must remain private and retain their identity;
unfinished installation, restore or authority review blocks import.
It rejects ambient `PG*` connection settings and uses the deployment's socket-only
database on port 5432, keeps one connection, checks
offline inventory and imports through Management, Execution, Runtime, Memory and
Calendar owner APIs. Interrupted uploads resume only when every unfinished object
matches this bundle, its real target Agent, current authority and complete request.
Other unsettled work blocks import. Exact retries reuse owner receipts; failure
preserves partial commits and maintenance. Ordinary backup/upgrade readiness is
unchanged; a migration retry enters the helper only after independently verifying
that writers and executors remain stopped, with PostgreSQL still available.

Apply restores current Notes and Tasks without replaying completion rules. Execution
atomically publishes Thread working files and converted extension manifests/private
state before first Host enrollment; Runtime imports only the returned location
references. Exact retries verify the source receipt, permissions and complete file
set, and reject changed destinations. It does not overwrite live files.

Apply does not select a default environment, authorize/connect extensions or resume
the platform. After offline import, start only Management and Execution, use real
extension inspection/configuration receipts, then start Runtime and applications.
MCP connection, subscriptions and application acceptance remain required. Captured
Calendar state requires the source Calendar MCP extension to have been selected;
a leftover file cannot authorize reactivating an old schedule.

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

`ConvertMCPExtension` turns selected v1 stdio and Streamable HTTP declarations
into the existing v2 manifest. Explicit executable, process cwd and Runtime WorkDir bindings preserve
separate source locations through a fixed argv launcher. Execution still owns
extension-private paths. Unsupported transports, Agent-wide defaults, unproved
resource absence, shell-managed environment values and unresolved argument paths
require separate conversion.
HTTP preserves the literal endpoint and resolves headers once from frozen source
Agent environment evidence; unknown values cannot select a fallback. This evidence
is private and bound to the bundle digest. Loopback reachability and redirect
dependence require target-environment acceptance; conversion performs no network
I/O and the target rejects redirects.
Conversion does not install dependencies, copy private state, connect MCP or
subscribe notifications; those require installation and behavior acceptance.

`internal/app/migration.ResolveConfig` resolves captured Home, Workspace, Agent
and explicit startup-file layers using the fixed source merge rules. Remote
imports require the exact source, declaring-file and startup-context cache
identity; conversion never fetches or republishes configuration. Reports omit
private provider values. The result describes disk configuration, not proven
inherited environment, authentication or target resource bindings. Unsupported
fields and missing or contradictory capture evidence stop resolution explicitly.

Current bundle policy preserves declared process environment keys using the same
Agent's captured effective environment, including empty values. The final merged
`load_dotenv` switch decides whether captured Workspace `.env` participates;
unknown or absent effective values stop conversion. Management encrypts these
initial values in the Agent layer with its definition and import receipt. This
is an initial effective snapshot, not a live link to source files. The operator
must prove that the captured configuration matches the source process and check
inherited-only dependencies separately; the importing process's environment is
never evidence. Source Supervisor authority requires explicit operator acceptance
and becomes the independent Main-only, same-Fleet Agent-management grant. It does
not grant raw YAML edits, secret access or operating-system process control.

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
all private settings agree. Each Agent retains its independent ordered model
chain; retained policy 2 proofs still require matching shared-primary tails. Source request caps
and effective capabilities remain unchanged; an implicit endpoint must be resolved
before planning. The plan omits secrets from JSON and grants no authority to
overwrite existing catalog entries. `PublishModels` applies adapter validation and
Management's atomic offline import to an empty deployment catalog and an unset
Tenant model policy. It writes catalog entries and the catalog allowlist,
preserves the platform default, and records stable UUIDs with their first route
authorization epochs. Exact retries return that receipt without resetting later
operator edits or rebinding historical messages to a newly authorized route.

`Prepare` joins retained reasoning messages to the fixed source's unique request
epoch, request and response, checks its digest and original reasoning, and matches
the unmodified captured catalog. Opaque Codex reasoning additionally requires a
proven account and unchanged endpoint. Apply binds that evidence to the first
publication receipt; it does not grant current access. Provider projection still
filters incompatible routes, and Codex still omits reasoning on its wire requests.
Unproved provenance stops conversion rather than inferring it from display names.

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
Other application job relationships require explicit verified bindings.
Current compaction metadata containing an image without a usable provider
path is rejected.

New bundles bind conversion policy 3: patch and buffered-write switches preserve
their source values and require an explicit target Files policy. Source journals
are checked for active buffered writes across compaction; unsettled or unproven
sessions require separate staged-session migration or source completion before
import. Preserving historical tool facts does not restore their active bytes.
Retained policy 2 keeps its original declarations and payload hashes on replay,
rejects new private configuration declarations and never backfills an already-used target.

The source input-tracking switch and its
proven scopes, message associations and checks enter the original Runtime import
transaction. Committed history restores checks even when settled queue rows were
pruned or a queue write was interrupted. Ended scopes remain distinct from checks;
missing evidence fails conversion. Apply
reports the conversion policy and the number of Threads with restored tracking.

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
