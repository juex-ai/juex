# Proposal: Worker Depth Limits and Thread Navigation

> English | [中文](worker-depth-navigation.zh.md)

Status: proposal; functionality is not implemented. Updated: 2026-09-09.
Implementation task: Taskline `bfb66d37-fc99-425b-b389-6836515b7af7`.
Recommendation: Strong.

## Problem and Goal

Workers can currently create more Workers without a nesting limit. A CLI Proxy
API investigation started by debaga on 2026-09-08 produced 310 Workers, reached
17 levels, and recorded 221,226,752 Worker tokens. A depth limit prevents
recursive expansion; it is not a Worker count, concurrency, or token budget.

Thread Explorer repeats the same icon on every row, uses excessive spacing,
and does not show parent relationships. The conversation area lacks a
persistent indication of the current Thread.

This proposal limits creation depth, makes the list compact, and uses parent
navigation instead of a tree.

## 1. Maximum Worker Depth

```yaml
modules:
  worker-threads:
    enabled: true
    max_depth: 1
```

Main has depth 0. Each parent edge adds one level:

| max_depth | Allowed structure | Creation permission |
| --- | --- | --- |
| 1 (default) | Main → Worker | Only Main can create |
| 2 | Main → Worker → Worker | Main and first-level Workers can create; second-level Workers cannot |

### Configuration Rules

- Omission defaults to 1. Only integers 1 and 2 are accepted; zero, negative
  values, values above 2, non-integers, and explicit null are configuration errors.
- Merge `max_depth` independently from `enabled`: changing either field preserves
  the other, and an explicit higher-layer value overrides a lower-layer value.
- `minimal` and `standard` retain their existing enablement defaults and do not
  change the default depth.
- A disabled module may retain a valid depth without starting execution
  capabilities. Invalid values still fail validation rather than being deferred
  until the module is enabled again.
- Only `worker-threads` accepts `max_depth`; other modules reject the field.
- Use existing configuration loading and restart activation. Do not add a
  separate hot-reload mechanism.

### Creation Boundaries

- A Thread at or beyond the limit does not compose the `worker-threads` module.
  The module does not exist for that Thread: none of its tools, schemas, prompts,
  guidance, resources, or lifecycle contributions are published or initialized.
  This applies to the entire module, not just `thread_create`.
- With `max_depth=1`, only Main has `worker-threads`. With `max_depth=2`, Main
  and first-level Workers have it; second-level and deeper Threads do not.
  Effective availability combines the configured module switch and the current
  Thread depth without rewriting the Agent's global configuration.
- A Worker within the new limit still executes normally. Its parent can manage
  it when that parent is below the limit and has the module enabled. The
  restriction concerns the Worker's own module for managing descendants.
  User-facing Thread Explorer, history, and storage management remain available
  because they are not contributions of that Thread's module.
- The server validates the persisted parent chain. Hidden tools or prompt
  guidance alone are insufficient. Model tools, HTTP, indirect CLI calls, and
  internal Runtime creation share the same rule.
- Model-created Workers derive their parent from the calling Thread; the model
  cannot forge it.
- Reject excessive depth before creating directories, writing the index,
  starting a child Runtime, or calling a child Provider. The error identifies
  `max_depth` clearly.
- Do not delete, migrate, or reparent existing deeper Threads; preserve their
  history and usage. If their parent is at the new limit, that parent loses
  Worker send, subscribe, stop, and other module capabilities. Existing children
  do not justify restoring a management-only module or tools on that parent.
- Host execution and lifecycle interfaces own recovery, execution, stopping,
  and retention management for these existing deeper Threads. Users access
  them through existing API, CLI, or UI operations, subject to Agent-level
  module enablement and existing execution constraints. Recovery and cleanup
  must not require composing the module on a capped parent. Settle existing
  subscriptions and result handoffs through normal shutdown and recovery rules;
  do not restore unavailable parent subscriptions. Further creation remains
  prohibited.
- Compute depth from persisted parents after restart or when opening a Worker
  independently. Archived ancestors do not reset depth. Missing parents or
  cycles reject creation.
- Preserve storage management when the module is disabled. Any permitted
  storage creation entrypoint also follows the depth limit, preventing a bypass
  when execution is enabled again.

## 2. Compact List and Parent Navigation

Keep Active and Archived as two flat sections with the existing ordering.

- Remove the repeated conversation icon and its reserved space.
- Reduce row padding and spacing between identity and statistics; remove the
  statistics indentation previously reserved for the icon.
- Start with approximately 48–56 px for a desktop row with two lines, then
  calibrate in the browser. Do not clip content to a fixed height.
- Retain status, Turn/Generation counts, pending inputs, context, cumulative
  usage, and usage details. Actions remain usable by touch and keyboard.

Example identity row:

```text
reviewer · #abc123   parent → main · #0
```

- Keep the `alias · #id` identity format. Use a restrained border or background
  for the parent marker. Main has no parent marker.
- Resolve the parent alias from the complete list snapshot, without reading
  individual Thread content or directories.
- Activating the marker scrolls the list to the parent row. It does not open
  the parent conversation, change the selected Thread, or trigger row actions.
- Center the parent in the visible area, focus its row, and highlight it for
  approximately three seconds. Repeated activation resets the timer; navigating
  to another parent transfers the highlight.
- Support navigation across Active and Archived. If the parent row is missing,
  show `parent → #id` with an unavailable explanation and no ineffective action.
- Long aliases may truncate with access to the full name. The marker may wrap
  on narrow screens without horizontal overflow.
- Support keyboard activation and reduced motion. Clear timers and highlighting
  when unmounting or switching Agents.

## 3. Agent and Current Thread Identity in the Navigation Bar

Reuse the Agent identity area to the left of Chat/Runtime as two compact lines;
no additional heading bar is inserted into the transcript:

```text
debaga                         Chat   Runtime
reviewer · #abc123  [Idle]
```

- Show the Agent name above and the current Thread's `alias · #id` below,
  immediately followed by that Thread's status badge.
- Keep the existing navigation height, currently 52 px. Reduce line height,
  second-line font size, and vertical gaps to fit both lines without consuming
  more conversation space. Truncate long names with access to their full text;
  the badge and Chat/Runtime tabs remain usable.
- Read identity from current Thread detail and status from the authoritative
  snapshot of that same Thread. Do not substitute another Thread's state or
  aggregate Agent activity. Archived Threads display Archived; loading or
  unknown status must not default to Idle.
- In the inspected code, `FleetStageHeader` uses `agentVisualState(agent)`, which
  combines `runtime_health` and `agent.activity.state`. This is an Agent display
  state and does not necessarily describe the viewed Thread. Change its data
  source rather than simply moving the existing badge.
- Keep Agent process health separate from Thread execution state. Process stops
  and connection problems retain the existing health/unavailability indicators;
  they must not masquerade as the current Thread being failed or idle.
- The navigation remains visible while the transcript scrolls. Refresh both
  lines when changing Thread or Agent, or updating an alias. Do not retain the
  previous identity or status while loading a new Thread.
- When Thread Explorer or Agent Runtime has no viewed Thread, show the page
  context (Threads/Runtime) on the second line, without a Thread status badge.
  Do not treat Agent `selected_status` or the last opened Thread as the current
  Thread. Fleet settings retains its own title.
- Do not duplicate the full statistics panel or add complex navigation.

## Ownership and Implementation Direction

- `internal/app/config`: parsing, defaults, layered merging, validation, and
  sparse overlays.
- `internal/features/workerthreads`: the module's complete capabilities and
  lifecycle contributions; the module is absent on Threads at the limit.
- `internal/framework/agent`: consistent creation constraints, with one depth
  check shared by execution entrypoints.
- `internal/framework/thread`: access to persisted topology, without reading
  YAML or depending on Feature configuration.
- `internal/app`: inject the resolved limit and compose modules per Thread using
  configuration and persisted depth. Do not construct `worker-threads` at the
  limit; keep execution of the Worker itself independent.
- `internal/entrypoints/agenthttp`: use the constrained entrypoint instead of
  implementing another depth algorithm.
- Thread Explorer: compact rows, parent markers, navigation, and highlighting.
- AppShell/FleetStageHeader: the fixed-height Agent/Thread identity area. The
  Thread page supplies current identity and status through the existing shell
  context, without adding a transcript heading bar.

Reuse existing parent metadata and the list index. Do not persist a new depth
field, infer depth from temporary Runtime pointers, or expand a general
permissions framework or tree component.

## Acceptance Criteria

1. Configuration coverage includes default 1, explicit 1/2, invalid values and
   types, misuse on other modules, independent enablement merging, imports, and
   Agent overlay save/read behavior.
2. Model/API/CLI cross-package tests cover both limits: permitted creation,
   rejected excessive depth, no residual Thread state, and no child Provider
   call. Inspect actual Provider requests, tool catalogs, and module lifecycles
   to prove that capped Threads receive no `worker-threads` tools, schemas,
   prompts, guidance, or resources, and never initialize the module.
3. Cover restart, independent restoration, archived ancestors, and invalid
   parent chains while preserving history and depth. Workers within the limit
   can accept work from parents that still have the module. Also test an
   existing depth-2 Worker after changing to `max_depth=1`: the depth-1 parent
   has no module, host interfaces recover, execute, stop, and manage the child,
   subscriptions and handoffs settle correctly, and retained history never
   restores the parent's tools or module.
4. Browser checks cover compact readable statistics, parent navigation across
   sections, focus, highlight expiry after three seconds, repeated activation,
   and missing parents.
5. Browser checks cover both identity lines, current Thread status, navigation
   changes, and unchanged 52 px height. Include an idle viewed Thread while
   another Thread works, archive/loading/disconnection, Thread Explorer/Runtime,
   narrow screens, and long aliases. Parent navigation also covers reduced motion.
6. Implementation follows [juex-localtest](../../.agents/skills/juex-localtest/SKILL.md)
   for automated verification and browser/API checks against rebuilt artifacts
   appropriate to the change.
7. Update the English and Chinese DOMAIN, DESIGN, and configuration contracts
   during implementation, and run `docs-check`.

## Out of Scope

No per-level count, total Worker count, concurrency, or token budget is added.
Do not clean up existing debaga Threads or introduce a tree UI. Publishing this
proposal does not deploy or restart local Agents.

## Subsequent Implementation

This document is design input for a subsequent Agent. Merging the proposal does
not implement the feature or automatically start the implementation task.
Recheck current code, domain contracts, and Taskline state before implementation.
After delivery, update the durable contracts and reconcile this proposal's
status so planned behavior is not mistaken for shipped functionality.
