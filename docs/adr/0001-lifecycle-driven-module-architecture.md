# ADR-0001: Lifecycle-Driven Module Architecture

> English | [中文](0001-lifecycle-driven-module-architecture.zh.md)

- Status: Accepted
- Date: 2026-08-21

## Context

Juex originally assembled Feature behavior through feature-specific calls in
`internal/app` and fields or callbacks in the runtime loop. That made lifecycle
ordering, Tool registration, prompt context, cleanup, and status projection
depend on several hand-maintained paths. A Feature could be hidden from one
surface while its constructor, process, subscription, or cleanup still ran.
Replacing a Feature also required changing Framework code.

The Agent, Session, Turn, Provider iteration, Tool call, compaction, and
shutdown lifecycles contain durable ordering rules. A replacement mechanism
must not let Feature code bypass admission, Tool declaration and outcome,
pending-input, Request Epoch, cancellation, compaction, or completion commits.
At the same time, product capabilities such as Goal, Notes, Skills, Hooks,
MCP, Observables, and Side Sessions should not be hard-coded into those
lifecycles.

External Extensions are a separate trust and deployment boundary. They are
selected resource bundles, not code that Juex loads into its Go process. The
Module architecture must preserve that distinction.

## Decision

### Separate Foundation, Framework, and Features

Juex uses three responsibility layers:

- **Foundation** owns business-agnostic primitives such as Provider adapters,
  Tool values and execution, Events and durable sinks, Session persistence,
  sandboxing, Artifacts, environment handling, and process management.
- **Framework** owns the stable Agent and runtime lifecycles, durable ordering,
  Module contracts, capability indexing, validation, and scoped lifecycle
  orchestration.
- **Features** own replaceable product capabilities and strategies. A trusted,
  compiled Feature may provide Tools, context, policy, observation, status, or
  scoped resources through narrow Framework-owned interfaces.

Dependencies point from Features to Framework to Foundation. Foundation does
not import Framework or Features, and Framework does not import concrete
Feature implementations. `internal/app` is the composition root: it may import
the concrete implementations needed to resolve configuration, filter disabled
factories before construction, and inject explicit dependencies before handing
sealed Module sets to Framework.

These layers describe responsibility and import direction, not a requirement
to move every package immediately. Package layout may converge incrementally
as long as ownership and dependency direction remain enforceable.

### Compose one sealed Module set per lifecycle scope

A Module has one stable identity and is registered once, even when it
implements several typed capabilities. Registration order is explicit and
deterministic. Framework validates identities, contributions, provenance, and
cross-scope Tool catalogs before publication, then seals the set so serving
code cannot mutate it.

Runtime and Session resources have distinct typed lifecycles. They start in
registration order, roll back or close in reverse order, attempt every cleanup,
and join failures with Module and phase identity. Disabled factories are
filtered before construction. A Session replacement builds and validates the
complete candidate set before atomically publishing it; the previous set
remains authoritative if candidate publication fails.

Feature dependencies use constructor injection at the composition root. When
one Feature optionally consumes another, the consumer defines the smallest
typed interface it needs. Framework does not acquire Feature-specific fields
or protocols to broker that collaboration.

### Bind flow-changing policy to durable checkpoints

Framework exposes separate typed interfaces for contribution, policy,
observation, and resource lifecycle. A policy can affect flow only at the
checkpoint whose durable preconditions give that decision a precise meaning:

- input policy runs after durable admission and cannot erase the accepted fact;
- Tool policy runs after the ordered batch and individual call start are
  durable, but before the external side effect;
- finish policy runs after the assistant response is durable and before a
  continuation or terminal completion is committed.

Framework retains the final ordering and recovery rules at every checkpoint.
Committed Events remain facts for projection, telemetry, and asynchronous
follow-up. Event subscribers and observation callbacks cannot retroactively
approve, reject, reorder, or replace the decision that emitted a fact.

### Keep external Extensions out of process

An external Extension remains a selected bundle of declarative resources and
managed commands. Trusted Juex adapters project its Skills, MCP servers, Hooks,
Observable definitions, and environment declarations into the same typed
Framework capabilities used by compiled Modules. Juex does not load Extension
Go plugins or dynamic libraries.

Extension provenance remains `ext:<name>`. Mutable state remains private to
the Agent and logical Extension under `JUEX_EXT_DATA_DIR`; it is not stored in
the selected installation. Disabling an Extension removes all of its adapted
capabilities and side effects without changing these ownership rules.

Memory therefore needs no Framework-specific slot. The first-party Memory
Extension is composed from the general Extension surfaces: MCP Tools, Hooks,
Skills/context, and Extension-private data.

## Consequences

- Feature enablement, disablement, replacement, and status derive from one
  validated composition rather than parallel registration and cleanup lists.
- Tool and context provenance are explicit, and duplicate or incomplete
  contributions fail before serving instead of exposing a partial catalog.
- Framework lifecycle tests can replace Modules without changing durable
  ordering, while Feature tests can target narrow contracts.
- `internal/app` remains responsible for explicit wiring. This is deliberate
  compile-time coupling at the composition boundary, not a runtime discovery
  mechanism.
- Adding a new flow-changing seam requires a demonstrated lifecycle need and a
  typed error/ordering contract; arbitrary interception is intentionally hard.
- External integrations keep process isolation and portable resource formats,
  at the cost of using managed MCP, Hook, Observable, and Skill adapters rather
  than calling third-party Go code directly.

## Rejected Alternatives

### Go plugins or dynamic libraries

Loading third-party code in process would collapse the Extension trust
boundary and introduce Go toolchain, ABI, platform, crash-containment, and
lifecycle-unload constraints. External resource adapters already provide the
required integration surfaces without making untrusted code part of the
runtime address space.

### A global string service locator

A `Resolve("service-name")` API would hide Feature dependencies, move missing
or incompatible dependency failures into runtime execution, and let code
bypass composition-time validation. Explicit constructor injection and
consumer-owned typed interfaces keep dependencies visible. If a future use
case requires runtime-selected dependencies, it should first introduce a typed
build-time resolver, not a Turn-time global registry.

### Universal lifecycle callbacks or Event-driven policy

One untyped callback surface would make phase preconditions, ordering, failure
semantics, and mutation authority implicit. Letting Event subscribers change
flow would also confuse committed facts with decisions and make replay
incoherent. Separate typed policies at fixed durable checkpoints preserve the
meaning of each decision; Events remain observation-only.

### A Memory-specific Framework slot

A `MemorySlot` would make one Feature part of the Framework contract and create
a precedent for special interfaces for every integration. Memory's needs are
already covered by general Extension capabilities and private Extension data.
A new Framework seam is justified only by a capability gap shared across
Features, not by one implementation.

### Priorities or a general dependency DAG

Numeric priorities obscure why one Module precedes another, while a dependency
solver adds cycle, tie-breaking, and partial-start semantics without a current
product need. Explicit registration order and constructor injection are easier
to validate and review. A DAG can be reconsidered after a concrete composition
cannot be expressed safely with these mechanisms.

### Hot reload or an everything-is-a-plugin runtime

Dynamic discovery and hot replacement would require new compatibility,
quiescence, dependency, and rollback contracts at arbitrary execution points.
Juex currently needs deterministic construction and atomic Runtime or Session
boundaries, not arbitrary in-place mutation. Restart and validated Session-set
replacement are the supported reconfiguration boundaries.

## Implementation Evidence

This ADR records the accepted decision rather than an implementation log. The
architecture was delivered in staged changes that provide repository-visible
evidence:

- Phase A established the Module kernel in [PR #443](https://github.com/juex-ai/juex/pull/443).
- Phase B migrated contributions in [PR #444](https://github.com/juex-ai/juex/pull/444).
- Phase C introduced typed lifecycle policies in [PR #445](https://github.com/juex-ai/juex/pull/445).
- Phase D completed scoped lifecycle, configuration, status, and composition
  cleanup in [PR #454](https://github.com/juex-ai/juex/pull/454).
- Automated dependency boundaries and replacement end-to-end coverage are
  tracked in [PR #455](https://github.com/juex-ai/juex/pull/455).

## References

- [Architecture: Module Sets And Lifecycle](../../ARCHITECTURE.md#22-module-sets-and-lifecycle)
- [Domain lifecycles](../../DOMAIN.md#lifecycles)
- [Philosophy: Prefer Explicit Surfaces](../../PHILOSOPHY.md#prefer-explicit-surfaces)
- [`internal/runtime/module`](../../internal/runtime/module/)
