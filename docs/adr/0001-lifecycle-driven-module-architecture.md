# ADR-0001: Lifecycle-Driven Module Architecture

> English | [中文](0001-lifecycle-driven-module-architecture.zh.md)

- Status: Accepted
- Date: 2026-08-21

## Context

Feature-specific wiring once spread construction, Tool registration, prompt
context, policy, status, and cleanup across App and runtime code. A disabled
Feature could still retain processes or side effects, while new Features
required edits to Framework lifecycles.

Agent, Thread, Turn, Provider iteration, Tool call, compaction, and shutdown
have durable ordering rules. Replaceable capabilities must participate without
bypassing those rules. External Extensions also need to remain outside the Go
process trust boundary.

## Decision

Juex separates three responsibilities:

- Foundation owns Provider-neutral values, persistence, Events, Tools,
  sandboxing, media/spool, environment, and process primitives.
- Framework owns Agent and Thread lifecycles, durable ordering, typed Module
  contracts, capability validation, and scoped orchestration.
- Features contribute Tools, context, policy, observation, status, or resources
  through narrow Framework-owned interfaces.

Dependencies point from Features to Framework to Foundation.
`internal/app` is the composition root: it selects enabled factories, injects
explicit dependencies, and hands validated Module sets to Framework.

A Module has one stable identity even when it implements several typed
capabilities. Registration order is deterministic. Module sets are validated
and sealed before serving. Resources start in registration order and close or
roll back in reverse order.

Agent and Thread resources use separate lifecycle scopes. Context Generation
changes rebuild prompt projection without replacing the Thread resource set.
Flow-changing policy is available only at Framework-defined durable
checkpoints; Events remain observation facts and cannot retroactively alter
the decision that produced them.

External Extensions remain selected declarative resources and managed
commands. Trusted adapters project their Skills, MCP servers, Hooks,
Observables, and environment into typed Framework capabilities. Juex does not
load third-party Go plugins or dynamic libraries. Mutable Extension data stays
under the owning Agent and Extension.

## Consequences

- Enablement, construction, publication, and cleanup derive from one validated
  composition.
- Tool/context provenance and lifecycle failures are explicit.
- Framework ordering can be tested with replacement Modules; Features can be
  tested through narrow contracts.
- `internal/app` intentionally retains compile-time coupling at the
  composition boundary.
- New flow-changing seams require a lifecycle need and a typed ordering/error
  contract.
- External integrations keep process isolation at the cost of using managed
  resource adapters.

## Rejected Alternatives

- Go plugins collapse the Extension trust boundary and add ABI, platform,
  crash-containment, and unload constraints.
- A string service locator hides dependencies and moves composition failures
  into Turn execution.
- Universal callbacks or Event-driven policy obscure phase preconditions and
  confuse committed facts with mutable decisions.
- Feature-specific Framework slots make one implementation part of the core
  contract when general capabilities are sufficient.
- Priority systems, dependency DAGs, and hot reload add ordering and rollback
  semantics without a current product need.

## References

- [Architecture: Dependency Direction](../../ARCHITECTURE.md#dependency-direction)
- [Domain Model](../../DOMAIN.md)
- [Philosophy](../../PHILOSOPHY.md)
- [Module contracts](../../internal/runtime/module/)
