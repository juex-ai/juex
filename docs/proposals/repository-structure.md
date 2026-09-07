# Repository Structure Proposal

> English | [中文](repository-structure.zh.md)

Status: overall structure, naming, and Foundation scope confirmed; package mapping awaits review. Not implemented. Date: 2026-09-07.

This document proposes ownership, package boundaries, and directory organization for discussion. It does not replace the current [architecture contract](../../ARCHITECTURE.md) or [ADR-0001](../adr/0001-lifecycle-driven-module-architecture.md). The production-consumer inventory has been completed from current source; package destinations and split boundaries are ready for pre-migration review.

## Problem and Goals

A thin `cmd/juex` entry point with implementation under `internal` follows [official Go guidance](https://go.dev/doc/modules/layout). The problem is that organization within `internal` does not sufficiently express the existing Foundation, Framework, and Feature architecture.

Examples in the current structure:

- `runtime` and `thread` sit beside `jsonl`, `frontmatter`, and `processmetrics`, obscuring the distinction between product execution and technical primitives.
- `cli`, `web`, and `fleetweb` sit beside execution packages without a transport grouping.
- Features are distributed across `modules`, `hooks`, `mcp`, `observable`, and `skills`.
- `llm` contains neutral message types, Provider contracts, and vendor implementations.
- `app` combines dependency composition with Main/Worker management and input admission.
- Chunked-write spans `modules/chunkedwrite`, `chunkedwrite`, and `tools`, requiring several locations to understand one feature.

Existing `internal/architecture/boundary_test.go` already classifies some packages and restricts imports. The issue is insufficient architectural visibility and some mixed responsibilities. These examples are not a complete dependency audit or evidence that all existing dependencies violate layering.

The goals are recognizable ownership from the tree, a clear home for new features, most feature changes contained within their owner, and execution mechanisms independent of concrete features and vendors.

## Scope

Retain one repository, one Go module, and `cmd + internal`. Adjust package ownership, interfaces, and dependencies while preserving product behavior, CLI/API contracts, configuration, and persistence formats.

Do not introduce a plugin system, general service locator, new features, or behavioral migrations. Any required external contract change needs separate discussion. Remove old paths after migration without compatibility forwarding packages.

## Classification Principles

The first directory level expresses architectural responsibility; the next uses concrete capability names. Grouping directories need not contain Go files, form packages, or become separate Go modules.

| Group | Responsibility |
| --- | --- |
| `app` | Implementation selection, dependency composition, startup, and shutdown |
| `entrypoints` | External operation entry points: CLI commands, HTTP requests, and SSE subscriptions |
| `fleet` | Agent registration, process supervision, and lifecycle management |
| `framework` | Agent/Thread execution, durable ordering, and extension contracts |
| `features` | Concrete capabilities contributed through Framework contracts |
| `providers` | Protocol implementations for external model services |
| `foundation` | Neutral contracts and shared lower-level mechanisms |

Avoid broad `core`, `common`, and `utils` groups. Do not initially create a central `domain` package: domain types and rules follow their behavioral owner instead of forming a shared model collection. A low dependency position does not imply absence of domain meaning; Thread owns product rules while JSONL owns file mechanics.

## Naming External Entry Points

In architecture, an adapter can encompass model services, storage, and protocol conversion, exceeding the intended CLI and HTTP/SSE responsibility here. The confirmed name is `entrypoints`, meaning the interfaces through which external callers operate JueX.

`cmd/juex/main.go` is the process startup entry point; `entrypoints/cli`, `entrypoints/agenthttp`, and `entrypoints/fleethttp` are operation entry points after startup. They handle argument parsing, request conversion, output formatting, and event subscriptions. They call internal operation interfaces without owning execution rules. SSE is an output of the same access protocol, not a separate feature.

Vendor protocols remain under `providers`, feature-owned MCP connections remain within their Feature, and OS service adaptation belongs to Fleet. Being an adapter does not place any of these under `entrypoints`.

## Foundation Contents and Admission Criteria

Foundation contains technical utility packages with specific purposes and neutral contracts shared by upper layers. It must not become the default home for all reusable code.

| Category | Candidate contents | Boundary |
| --- | --- | --- |
| Technical mechanisms | JSONL append and recovery, process identity and metrics reads | Do not interpret Thread, Turn, or Feature business rules |
| Neutral contracts | LLM message values and Provider interfaces, Tool input/output contracts | Exclude vendor implementations and concrete file or Shell tools |
| Shared mechanisms | Generic event delivery, safe execution, and file access mechanisms | Domain event schemas, authorization decisions, and lifecycle rules remain with their semantic owners |

Admission requires a specifically named responsibility, an independent foundational contract or a clear foundational use across modules, and independence from upper-layer implementations and lifecycle decisions. Short code, multiple callers, or uncertainty about placement are insufficient reasons.

For example, JSONL implements durable file append while Thread decides when to commit Generation facts. Tool contracts describe call inputs and outputs; Runtime decides ordering and cancellation, and Features implement concrete tools. Feature-private parsing and state helpers stay within the Feature.

The candidate table does not authorize moving entire packages. Contracts, mechanisms, and policies within existing `events`, `tools`, and `sandbox` require separate examination. The confirmed scope retains one `foundation` group with concrete package names distinguishing the two kinds of content. The complete inventory below assigns ownership based on actual responsibilities.

## Candidate Tree

This illustrates key groups, not a final inventory of every existing package.

```text
cmd/juex/main.go
internal/
  app/
    config/
    modulecatalog/
    eventcatalog/
    providerreadiness/
  entrypoints/
    cli/
    agenthttp/
    fleethttp/
    webassets/
  fleet/
  framework/
    agent/
    agentstate/
    endpoint/
    status/
    thread/
    runtime/
    module/
    prompt/
  features/
    filetools/
    applypatch/
    filesearch/
    shell/
    contextcontrol/
    workerthreads/
    extensions/
    operatingcontext/
    goal/
    notes/
    scratchpad/
    agentsmd/
    skills/
    hooks/
    mcp/
    observables/
    chunkedwrite/
  providers/
    openai/
    anthropic/
  foundation/
    llm/
    tools/
    events/
    jsonl/
    sandbox/
    command/
frontend/
tests/
scripts/
docs/
```

Providers adapt model services; they are not equivalent to Features enabled through Modules. A separate group distinguishes vendor implementations from neutral Provider contracts.

## Complete Package Ownership Inventory

Inspection baseline: 2026-09-07, current workspace at `HEAD d962f4a`. Covers all **59 package directories containing production Go files** under `cmd` and `internal`, including nested packages rather than only immediate children.

Production consumers are direct importing packages from non-`_test.go` files under current `cmd`/`internal`. Scanning includes OS-specific and build-tagged sources, not a claim that all paths run together. Indirect consumers, dynamic interface calls, and test consumers are excluded. Responsibilities were checked against source files, exported interfaces, and module documentation. Except for `cmd/juex`, current packages, consumers, and destinations omit `internal/`. Destinations are proposed ownership; comma-separated targets require splitting before migration. Multiple current packages may merge into one destination.

This is a pre-migration architectural snapshot, not a permanent package inventory. Remove it after migration, leaving code, boundary tests, and concise architecture documentation authoritative.

### Composition, entry points, and Fleet

| Current package | Responsibility | Current direct production consumers | Target ownership |
| --- | --- | --- | --- |
| `cmd/juex` | Process entry, network bootstrap, and sandbox subprocess entry | Process launch | `cmd/juex` |
| `app` | Composition, Agent orchestration, Feature registration, and shared operations | `cli`, `fleet`, `web` | `app`, `framework/agent`, `features/workerthreads`, `features/extensions` |
| `cli` | Cobra commands, service startup entry, and terminal output | `cmd/juex` | `entrypoints/cli`, `app` |
| `config` | Layered loading, validation, auth resolution, and config publication | `app`, `bundle`, `cli`, `fleet`, `fleetweb`, `observable`, `providerreadiness`, `web` | `app/config` |
| `fleet` | Registration, supervision, restart, GC, and configuration operations | `cli`, `fleetweb` | `fleet` |
| `fleetservice` | Fleet OS service installation and management | `cli` | `fleet/service` |
| `fleetweb` | Fleet HTTP, directory browsing, activity subscriptions, and proxying | `cli` | `entrypoints/fleethttp` |
| `providerreadiness` | Model configuration, credential, and connectivity diagnostics | `cli` | `app/providerreadiness` |
| `web` | Single-Agent HTTP/SSE, status projection, runtime composition, and SPA embedding | `cli`, `fleetweb` | `entrypoints/agenthttp`, `entrypoints/webassets`, `app` |

### Execution, status, and domain storage

| Current package | Responsibility | Current direct production consumers | Target ownership |
| --- | --- | --- | --- |
| `agentstate` | Agent identity, Workspace binding, registry records, and lifecycle locks | `app`, `cli`, `config`, `fleet` | `framework/agentstate` |
| `bundle` | Thread debug archives, manifests, and runtime snapshot export | `cli` | `framework/threadbundle` |
| `endpoint` | Agent endpoint binding, discovery, identity probes, and control | `cli`, `fleet`, `fleetweb`, `web` | `framework/endpoint` |
| `eventcatalog` | Event schema validation, concrete catalog composition, and visibility metadata | `app`, `web` | `foundation/events`, `app/eventcatalog` |
| `eventmedia` | External Observation attachment parsing, validation, and persistence | `app`, `observable` | `framework/observationmedia` |
| `observability` | Readable Thread logs derived from runtime events | `app` | `framework/threadlog` |
| `prompt` | System prompt assembly from Module contributions | `app`, `runtime`, `runtime/contextbudget` | `framework/prompt` |
| `provenance` | Provider request selection identity, safe digests, and events | `app`, `eventcatalog`, `runtime`, `runtime/module` | `framework/provenance` |
| `runtime` | Input/Turn lifecycle, Provider loop, recovery, compaction, and tool execution | `app`, `eventcatalog`, `statusapi`, `web` | `framework/runtime`, `features/contextcontrol` |
| `runtime/contextbudget` | Context budgets, history selection, and previews | `runtime` | `framework/runtime/contextbudget` |
| `runtime/module` | Capability contracts, registration, startup, activation, and shutdown | `app`, `eventcatalog`, `hooks`, `mcp`, `modules/agentsmd`, `modules/builtintools`, `modules/chunkedwrite`, `modules/goal`, `modules/notes`, `modules/operatingcontext`, `modules/scratchpad`, `modules/shelltools`, `modules/skills`, `observable`, `prompt`, `runtime`, `runtime/contextbudget` | `framework/module` |
| `runtime/module/state` | Module resource ownership, leases, and retirement | `app`, `runtime/module`, `runtime/workmem` | `framework/module/state` |
| `runtime/policy` | Compaction and tool-output policy values | `config`, `runtime`, `runtime/contextbudget` | `framework/runtime/policy` |
| `runtime/workmem` | Goal/Notes stores, events, and file helpers | `app`, `eventcatalog`, `modules/goal`, `modules/notes`, `runtime`, `web` | `features/goal`, `features/notes`, `framework/module/state` |
| `statusapi` | Runtime status DTOs, conversion, and activity snapshots | `fleet`, `fleetweb`, `web` | `framework/status` |
| `thread` | Thread metadata, Generation history, indexes, and archive | `app`, `bundle`, `cli`, `eventcatalog`, `fleetweb`, `runtime`, `runtime/workmem`, `web` | `framework/thread` |
| `usermedia` | User image input validation, Thread scoping, and storage | `app`, `web` | `framework/inputmedia` |

### Features and feature declarations

| Current package | Responsibility | Current direct production consumers | Target ownership |
| --- | --- | --- | --- |
| `chunkedwrite` | Chunked-write lifecycle facts | `modules/chunkedwrite`, `tools` | `features/chunkedwrite` |
| `extensions` | Extension discovery, manifests, and declared resource catalog | `app` | `features/extensions` |
| `frontmatter` | Skill frontmatter parsing | `skills` | `features/skills/internal/frontmatter` |
| `hooks` | Command Hook configuration, execution, and Module lifecycle adaptation | `app`, `config` | `features/hooks` |
| `mcp` | MCP configuration, connections, tool catalogs, notifications, and readiness | `app`, `cli`, `web` | `features/mcp` |
| `modulecatalog` | Concrete capability IDs, preset defaults, and capability inventory | `app`, `config`, `hooks`, `mcp`, `modules/agentsmd`, `modules/builtintools`, `modules/chunkedwrite`, `modules/goal`, `modules/notes`, `modules/operatingcontext`, `modules/scratchpad`, `modules/shelltools`, `modules/skills`, `observable`, `runtime`, `web` | `app/modulecatalog`, `respective Features` |
| `modules/agentsmd` | Automatic AGENTS.md loading and context contribution | `app` | `features/agentsmd` |
| `modules/builtintools` | Module wrappers for basic files, Patch, and search | `app` | `features/filetools`, `features/applypatch`, `features/filesearch` |
| `modules/chunkedwrite` | Chunked-write tools, recovery, and history folding | `app` | `features/chunkedwrite` |
| `modules/goal` | Goal tools, finish policy, and compaction contributions | `app` | `features/goal` |
| `modules/notes` | Notes tools, context, and compaction contributions | `app` | `features/notes` |
| `modules/operatingcontext` | Working directory, OS, and time context | `app` | `features/operatingcontext` |
| `modules/scratchpad` | Thread working-file resources and guidance | `app`, `web` | `features/scratchpad` |
| `modules/shelltools` | Shell tools, session lifecycle, and context | `app` | `features/shell` |
| `modules/skills` | Module adaptation for Skill tools and context | `app` | `features/skills` |
| `observable` | Observation sources, schedules, batching, delivery, and tools | `app`, `eventcatalog`, `web` | `features/observables` |
| `skills` | Builtin and file Skill discovery, loading, and filtering | `app`, `cli`, `modules/skills` | `features/skills` |

### Foundational mechanisms and model implementations

| Current package | Responsibility | Current direct production consumers | Target ownership |
| --- | --- | --- | --- |
| `artifact` | Artifact path safety, atomic storage, and integrity checks | `app`, `bundle`, `eventmedia`, `llm`, `modules/shelltools`, `observable`, `runtime`, `tools`, `usermedia`, `web` | `foundation/artifact` |
| `cancellation` | Cancellation causes and OS signal classification | `app`, `cli`, `errorclass`, `runtime`, `runtime/module`, `tools`, `web` | `foundation/cancellation` |
| `environment` | Immutable environment snapshots, dotenv, and child environment resolution | `app`, `bundle`, `cli`, `config`, `extensions`, `hooks`, `mcp`, `observable`, `tools`, `web` | `foundation/environment` |
| `errorclass` | Error classification and stable error kinds | `app`, `cli`, `mcp`, `runtime`, `tools` | `foundation/errorclass` |
| `events` | Event envelopes, Bus, schema interfaces, and commit-before-publish mechanics | `app`, `eventcatalog`, `modules/goal`, `modules/notes`, `observability`, `observable`, `provenance`, `runtime`, `thread`, `toolevents`, `web` | `foundation/events` |
| `homestore` | Atomic file publication, directory sync, and file locks | `agentstate`, `config`, `endpoint`, `fleet`, `fleetservice`, `jsonl`, `runtime`, `runtime/module/state`, `thread` | `foundation/homestore` |
| `jsonl` | Durable JSONL append, tail repair, and bounded reads | `thread` | `foundation/jsonl` |
| `llm` | Neutral messages, Provider contracts, vendor protocols, model health, and display | `app`, `cli`, `config`, `eventcatalog`, `fleet`, `hooks`, `modules/chunkedwrite`, `provenance`, `providerreadiness`, `runtime`, `runtime/contextbudget`, `runtime/module`, `statusapi`, `thread`, `toolevents`, `tools`, `usermedia`, `web` | `foundation/llm`, `providers`, `providers/openai`, `providers/anthropic`, `framework/modelhealth`, `entrypoints/cli` |
| `netbootstrap` | Startup DNS and TLS root fallbacks | `cmd/juex` | `foundation/netbootstrap` |
| `processidentity` | Cross-platform process start identity reads | `endpoint`, `fleet` | `foundation/processidentity` |
| `processmetrics` | Process CPU and memory sampling | `cli`, `fleet`, `fleetweb` | `foundation/processmetrics` |
| `sandbox` | File access constraints, command isolation, and platform execution backends | `app`, `cli`, `cmd/juex`, `config`, `eventmedia`, `modules/chunkedwrite`, `modules/skills`, `observable`, `tools`, `web` | `foundation/sandbox` |
| `statusstream` | Replaceable snapshots, subscriptions, and bounded replay | `runtime`, `statusapi` | `foundation/statusstream` |
| `toolevents` | Tool call fact contracts and output-delta envelopes | `app`, `eventcatalog`, `observability`, `runtime`, `thread`, `tools`, `web` | `foundation/toolevents` |
| `tools` | Tool contracts, registration and call mechanics, plus file, search, and Shell implementations | `app`, `cli`, `mcp`, `modules/builtintools`, `modules/chunkedwrite`, `modules/goal`, `modules/notes`, `modules/shelltools`, `modules/skills`, `observable`, `runtime`, `runtime/module` | `foundation/tools`, `features/filetools`, `features/applypatch`, `features/filesearch`, `features/shell`, `features/chunkedwrite`, `foundation/command` |
| `version` | Build version metadata | `bundle`, `cli`, `tools`, `web` | `foundation/version` |

### Test Packages and Non-Go Resources

| Current location | Consumers and responsibility | Target treatment |
| --- | --- | --- |
| `internal/architecture` | Tests only: scan production imports and enforce layers | Move to `tests/architecture` and check complete classification by new groups |
| `tests/e2e` | Cross-package, CLI/API, and runtime behavior tests | Retain; update imports and build paths with the behavior being moved |
| `tests/e2e/testdata/env-mcp` | MCP environment fixture launched by E2E | Retain; exclude from product production packages |
| `tests/eval` | Capability evaluation and test support | Retain; its ordinary `.go` files are also excluded from product production consumers |
| `frontend` | Browser consumes HTTP/SSE rather than Go imports | Retain source location and external protocols |
| `internal/web/dist` | Frontend build output embedded in Go | Move with `embed.go` to `entrypoints/webassets/dist`; update build, install, CI, and ignore rules |
| `internal/skills/builtin` | Builtin guides embedded by `skills/builtin.go` | Move with Skills to `features/skills/builtin`, preserving resource meaning |
| Package `_test.go` files and READMEs | Behavior verification and necessary contracts | Move or split with the behavioral owner; keep language peers aligned |

Scripts, release resources, and root documents retain their directories, with affected paths updated. Temporary directories such as `.tmp`, generated files, and third-party dependencies are not product package mapping subjects.

### Dependencies Requiring a Split First

These details explain the inventory's multiple destinations and the dependencies to resolve before mechanical relocation. Preserve existing Module IDs, JSON fields, state files, and CLI/API behavior throughout.

1. **App and execution orchestration.** Provider/Module factories and enablement selection in `app.go` and `agent_runtime.go` stay in App. Admission, recovery, Thread leases, Main/Worker management, and subscription mechanisms move to `framework/agent`. Split the manager in `worker_threads.go` from its model tools and Module wrapper, which belong to `features/workerthreads`. Framework receives resolved parameters and construction callbacks without importing App. Shared routing in `slash.go` belongs to Agent operations; Goal-specific guidance belongs to Goal, explicitly connected by App.
2. **Configuration and presets.** Move `config` to `app/config`, an application configuration boundary rather than Foundation. Hook configuration may use a declaration-only `features/hooks/config` subpackage while execution stays in its parent; validation must not construct Feature resources. Replace the `config.ShellProfile` dependency in `observable/manager.go` with injected resolved execution parameters from `foundation/command`, removing the Feature-to-App configuration dependency. Put preset inventory in `app/modulecatalog`; each Feature declares its stable ID, referenced by that inventory. Pass inventory into configuration rather than depending on factories or creating a cycle. An ID without an implementation, such as current Memory, does not authorize adding a feature.
3. **Goal/Notes state.** Move Stores, state values, and events from `runtime/workmem` into `features/goal` and `features/notes`. `runtime/thread_state_modules.go` currently returns concrete Stores; moving them must not leave Runtime importing Features. App connects Feature read interfaces and projects state to entry points, while Runtime retains only execution-required Module capabilities. Generic resource writing and retirement belong to `framework/module/state`; reuse `foundation/homestore` for general atomic file mechanics while preserving persistence semantics. Do not create another generic workmem layer for these two Features.
4. **Context Control.** Move model tools, capability identity, and reminder contributions from `runtime/context_control.go` into `features/contextcontrol`, requesting transitions through a narrow interface. Runtime retains transitions, automatic compaction, commits, and recovery. This moves tool contributions without changing `/new`, `/compact`, or enablement semantics.
5. **LLM.** Put `types.go` and Provider call interfaces in `foundation/llm`. `provider.go` also contains SDK dependencies, construction, and error classification and must split by symbol. Vendor files go to their respective Provider packages; shared construction selection goes to `providers`. The neutral layer must not import SDKs or vendors. Profile value contracts remain neutral; vendor default resolution belongs to Providers. Move `model_health.go` to `framework/modelhealth`, terminal formatting to CLI, and Provider-shared media encoding helpers to `providers/internal`. Keep neutral transcript validation and message projection near LLM contracts.
6. **Tools and implementations.** Put `registry.go`, `schema.go`, `capabilities.go`, and generic call-result/output contracts in `foundation/tools`; Runtime retains Provider ordering and Turn scheduling. Merge file, Patch, search, Shell session, and chunked-write implementations into their respective Features. Remove the universal builtin factory's implementation dependencies and compose each Feature in App. Execution parameters and mechanisms actually shared by Shell and Observables belong to `foundation/command`; Shell retains TTY sessions. Prefer existing Sandbox for shared file-path mechanisms. Retain result sanitation and media contracts at the foundation only where actually shared.
7. **Events.** Move generic validation from `eventcatalog/catalog.go` into `foundation/events`, and concrete aggregation in `builtin.go` into `app/eventcatalog`, with schemas and validators provided by their behavioral owners. Decoding core and historical Feature events must not disappear when a Feature is disabled; static schema registration does not start Features. Generic commit-before-publish mechanics in `events` can remain in Foundation. `toolevents` belongs there as a fact contract used by multiple consumers; it does not decide when Tool calls start or finish.
8. **HTTP, Fleet, and status.** Move resource construction from `web/runtime.go` into App and inject service interfaces into HTTP. Replace App-dependent configuration validation in `fleet/web_backend.go` with an injected configuration operation interface so Fleet does not import the composition root. Move `statusapi` to `framework/status`, retaining shared status values, projections, and snapshots; HTTP codes and SSE frames belong to entry points. Feature access in `web/files.go`, `handlers.go`, and `observables.go` uses injected service interfaces without bypassing lifecycle rules. Separate SPA embedding into `entrypoints/webassets` so Fleet HTTP need not depend on the Agent HTTP server for static assets.
9. **Extensions and media.** Extension discovery, resource interpretation, and private state belong to `features/extensions`. App selects and connects Skills/MCP/Hooks/Observables from declarations; Framework does not interpret Extension details. Move `usermedia` and `eventmedia` to Framework's user-input and Observation-attachment admission packages, preserving Thread/Agent scope rules. Artifact/Sandbox retain byte storage and safe paths.

Only `skills` currently imports `frontmatter` in production, so place it in `features/skills/internal/frontmatter`. The earlier Foundation example illustrated parsing mechanics, not established shared ownership. JSONL also has one direct consumer, Thread, but has an independent generic persistence interface and failure semantics, warranting its explicit foundational role.

## Dependency Rules

- Foundation does not depend on Framework, Features, Provider implementations, or transports.
- Framework uses neutral contracts without importing concrete Features or Providers.
- Features participate through explicit Framework interfaces; collaboration uses contracts rather than each other's private implementations.
- Providers implement neutral interfaces without depending on application orchestration.
- Entrypoints call application operation interfaces and own protocols, argument conversion, and presentation. They do not independently decide lifecycles or directly mutate domain state.
- App may reference concrete implementations for final composition; it must not accumulate every cross-package operation.
- Fleet manages Agent processes; Agent Framework manages execution within a process. Their interaction uses explicit interfaces.

Interfaces belong with their semantic owner or consumer, not a universal `interfaces` package. Directory nesting alone does not restrict Go imports. Boundary tests should cover the new groups and visibly flag unclassified packages.

## Implementation Sequence and Verification

1. Use the completed package ownership and direct production-consumer inventory as the baseline to review the split boundaries above; recheck new packages and changed dependencies before implementation.
2. Perform path-only migrations for clear ownership, updating references, builds, and embedded-resource paths. Avoid mixing behavioral refactoring into the same batch.
3. Address `llm`, `app`, and distributed Feature implementations separately, keeping each batch buildable and verifiable.
4. Update boundary checks, root architecture documentation, and necessary module documentation; remove obsolete paths and explanations.

Follow the repository [local verification Skill](../../.agents/skills/juex-localtest/SKILL.md) for code changes, with coverage proportionate to cross-package effects. Visible Web behavior changes require browser verification. Run `make docs-check` for bilingual documentation.

## Trade-offs and Discussion Points

Grouping lengthens import paths and creates substantial mechanical diffs. The benefits must come from clearer ownership and dependency enforcement; adding directories alone is not completion.

Confirmed directions:

1. Organize the first level by architectural responsibility.
2. Retain Fleet as a distinct subsystem owning process supervision.
3. Narrow App to composition and move execution orchestration into Framework; specific interfaces and package splits still require analysis.
4. Give Provider implementations a separate group.
5. Name external operation entry points `entrypoints`.
6. Foundation includes neutral contracts and lower-level technical mechanisms, organized by concrete package names.

Package mappings and nine split boundaries are now complete for review. The next discussion should focus on these concrete assignments. Confirmation of overall direction does not authorize code migration in this turn; this turn updates the proposal only.
