# Juex Philosophy

Juex is an agent runtime for local, inspectable work. Its design bias is to
make the agent loop understandable: inputs are plain files, tools are explicit
contracts, events are observable, and state is stored where the user can read
or delete it.

## Principles

### Keep The Runtime Small

The core loop should stay easy to reason about: build a prompt, call a
provider, execute requested tools, persist history, emit events, and repeat
until the turn is done. New behavior belongs in the core only when it is needed
by that loop or by a user-facing workflow already in the product.

### Prefer Explicit Surfaces

Commands, API routes, files, and JSON shapes are contracts. They should be
stable, documented, testable, and simple enough for another agent to call
without guessing. Avoid hidden magic when a small command or file makes the
state visible.

### Bind State To The Agent

Each workspace has one explicit marker under `.juex/`; its sessions, memory,
history, and logs live together in the agent registry under `JUEX_HOME`.
Workspace config, artifacts, and observable state remain near the work, while
agent guidance and skills live under `.agents/`. This split lets identity-owned
state survive a workspace move without hiding which workspace owns it.

### Use Providers Behind One Model

Provider SDKs are implementation details. The rest of the runtime works with
Juex message, block, tool, usage, and stop-reason types. Provider-specific
features such as reasoning blocks are preserved, but they should not leak into
unrelated packages.

### Treat Tools As Interfaces

Builtin tools and MCP tools expose small schemas and deterministic names.
The runtime should favor fewer, clearer tools over broad surfaces that invite
hallucinated calls. A tool result is part of the conversation contract and must
be persisted in order.

### Make The Web UI A Control Surface

The web UI exists to inspect sessions, run turns, interrupt work, and manage
session history. It should stay close to the JSON/SSE API instead of becoming a
separate app model. React state mirrors server state; the server remains the
source of truth.

### Defer Until It Hurts

Search, collaboration, attachments, richer permissions, and hosted deployment
are all plausible, but not default. Add them when a concrete workflow requires
them and when the implementation can stay small enough to test and explain.

## Trade-Offs

- Single binary over multi-service architecture: easier install and easier
  mental model, at the cost of fewer deployment knobs.
- Standard library first in Go: less dependency drift, at the cost of writing
  small protocol adapters ourselves.
- Marker-bound agent homes: state survives workspace moves and supports a
  central fleet registry, at the cost of an explicit identity binding and
  migration step.
- Synchronous turn loop with parallel tool calls: simple ordering and tests,
  while still allowing independent tool calls inside one model response.
- JSONL history: durable and append-friendly, with heavier reads when a full
  transcript is needed.
