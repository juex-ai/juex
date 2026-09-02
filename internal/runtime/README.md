# Runtime Lifecycle

> English | [中文](README.zh.md)

This package owns the Framework-level Turn loop. Product meanings are defined
in [`DOMAIN.md`](../../DOMAIN.md); repository ownership is defined in
[`ARCHITECTURE.md`](../../ARCHITECTURE.md).

## Ownership

- `internal/thread` owns Thread identity, the single Journal, replay,
  projections, Input states, Generations, archive/delete, and timeline paging.
- `internal/app` assembles one Runtime per active Thread, owns process-level
  resources, runs Workers, and converts transport or Observation delivery into
  Runtime Inputs.
- `internal/runtime` owns admission, Provider iteration, Tool batches, policy
  checkpoints, Context Generation transitions, completion, cancellation, and
  authoritative runtime status projection.
- `runtime/module` defines registered prompt, lifecycle, policy, and Tool
  contributions.

The Engine is Thread-scoped. Main and Worker instantiate the same Engine; Main
only gains additional Agent-level Observation and Worker-management modules.

## Commit Order

Durable facts are committed to the owning Thread Journal before status, Web,
subscriber, or Hook observation. One Journal commit may contain an ordered
atomic fact batch; consumers must never observe a prefix of that batch.

### Input and Turn

1. Persist `input.accepted` with a stable Input ID.
2. If the Thread can start work, persist Turn admission and Input assignment in
   one ordered transition. Otherwise the Input stays pending.
3. Project the admitted Input into Provider conversation exactly once.
4. Run prompt contributors and request recitation, then call the Provider.
5. At Provider iteration boundaries, assign durable pending Inputs in their
   accepted order.

Input identity survives retries and restart. Turn identity does not replace it.
An accepted Input is `pending`, `assigned`, `processed`, `failed`, or
`cancelled` according to Journal facts; transport success alone is never the
recovery authority.

### Tool Batch

1. Commit the complete ordered `tool.requested` batch after the Provider
   response.
2. Commit `tool.running` before any policy or handler action.
3. Commit any policy-transformed effective input before later execution.
4. Execute independent calls concurrently, retaining Provider order.
5. Commit each exact Provider-visible terminal outcome before appending the
   result message or calling the Provider again.

Recovery rules are intentionally conservative:

- a requested but not started call becomes `TOOL_NOT_STARTED`;
- a started call without a durable outcome becomes `TOOL_OUTCOME_UNKNOWN` and
  is not automatically retried;
- a durable terminal outcome is replayed byte-for-byte as the Provider-visible
  result, without re-running projection logic.

### Completion

Finish policies run only after the Assistant response is durable. A valid
continuation is persisted as a new Input before it can continue the loop.
`turn.completed`, `turn.errored`, or `turn.cancelled` is committed last. Status
and subscribers only report committed terminal facts.

## Context Generations

`context_new` and `context_compact` are lifecycle requests, not direct mutation
inside a Tool handler. The Runtime applies them at a safe boundary:

- `new`: start a Generation without summary, clear Goal and Notes, retain
  Scratchpad;
- `compact`: create a summary, start a Generation from that summary, retain
  Goal, Notes, and Scratchpad.

Both persist a UI-visible system activity record. These records are excluded
from Provider context. Prompt recitation reports current context tokens and
percentage so the Agent can choose the appropriate transition.

## Observation Policy

External automated events use `observable.Observation`. `DeliverObservation`
is enabled only for Main Thread `0`. Worker delivery fails before Input
acceptance. Provider-independent MCP clients may be Agent-owned and shared, but
Tool invocation and emitted facts remain Thread-scoped.

## Failure Boundaries

- Journal append failure publishes nothing.
- Projection write failure after a durable append returns a stale-projection
  error; replay rebuilds it from Journal facts.
- Invalid or torn Journal tails are truncated only to the last valid complete
  commit. A corrupt committed prefix fails loud.
- Runtime restart reconstructs pending Inputs, active Generation, status, and
  Tool recovery only from Journal facts.
- Working Threads cannot archive or delete. Main cannot archive, rename, or
  delete. A parent cannot be removed while active descendants violate the
  topology rule.

## Tests

High-signal suites:

- `internal/thread`: Journal atomicity, replay, index rebuild, reverse paging,
  lifecycle constraints, and protocol repair;
- `loop_test.go`: admission, pending drain, Provider/Tool ordering, Finish
  policies, cancellation, and terminal failures;
- `context_control_test.go`: Agent-triggered `new`/`compact` requests;
- `thread_runtime_test.go`: coherent Engine bundle publication and recovery;
- `internal/app/worker_threads_test.go`: Worker lifecycle and subscriptions;
- `tests/e2e`: cross-package durable Input, Web, restart, and Tool recovery.

```bash
make verify-focused PKGS="./internal/thread ./internal/runtime ./internal/app"
make verify-final RACE=1 COMPACTION=1
```
