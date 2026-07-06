# JueX Observables Product Design

Date: 2026-07-06
Status: Proposed

## Product Language

Observables are JueX's perception layer.

- An **Observable** is a configured source that JueX observes, usually a
  long-running command whose stdout or stderr represents external change.
- An **Observation** is the bounded, durable batch emitted by an Observable.
- Observations enter the active primary session as external context. They do
  not target side sessions and they do not rewrite command output into normal
  tool results.

The naming is intentionally product-specific. JueX should not present this as
generic process monitoring. The feature is about letting the agent perceive
workspace-local signals: CLI notifications, alert checks, test failures, build
watchers, or low-frequency event streams.

## Problem

Several useful agent workflows depend on changes that happen outside the
current provider turn:

- A CLI such as `lark-cli` or `chanwire` keeps a websocket open and prints a
  notification when new data arrives.
- A local command periodically checks a platform or service and prints an alert
  only when the alert condition changes.
- A build or test watcher prints failures while the user and agent are working
  elsewhere.

The current `exec_command` tool can run long-lived commands and stream live
output to runtime events, but that output is not automatically delivered to the
agent. That is correct for raw stdout: most stdout is too noisy, too large, and
too low-level to become model input. The missing product capability is a
trusted, bounded, reliable path from selected command output to the active
primary agent session.

## Goals

- Let a workspace configure command-backed Observables under `.juex/`.
- Start configured Observables when a JueX process starts, scoped to the
  current workspace and active primary session.
- Capture stdout and stderr from Observable commands.
- Optionally filter or parse command output before delivery.
- Batch Observations with required time and size limits before sending them to
  the runtime.
- Persist Observations before delivery so they can be queried, deduplicated,
  and recovered after restart.
- Deliver Observations to the same pending-input path used for other external
  events when a turn is already active.
- Expose Observable management tools to the agent: list, create, start, stop,
  delete, and list observations.
- Expose an Observables page in the Web UI with list, detail, recent
  observations, and deletion controls.
- Reuse the workspace shell profile, environment expansion, sandbox policy,
  output hygiene, and artifact persistence rules where appropriate.

## Non-Goals

- No general automation scheduler in the first version.
- No remote service or hosted queue.
- No global user-level Observables in the first version.
- No restart policy in the first version. If a command exits, record the exit
  and emit an exit Observation when configured.
- No `autostart` flag in the first version. Every configured Observable starts
  on JueX process startup. Runtime stop is temporary; delete is permanent.
- No in-process third-party extension code.
- No direct delivery of every raw output delta to the provider transcript.
- No editing tool for Observables. Edits happen by delete plus create.

## User Stories

### Low-frequency Notification CLI

As a user, I can configure `lark-cli watch --json` as an Observable. It already
emits only real notification events, so I do not need filters. JueX batches
those JSONL events, maps fields into Observation kind/severity/content, and
delivers the result into the active primary session.

### Build Failure Watcher

As a user, I can configure a build or test command and filter for failure
markers such as `FAIL`, `panic:`, or `error:`. JueX sends only matching output
windows as Observations, with `kind=test_failure` and `severity=error`.

### Alert Poller

As a user, I can configure a script that checks an external platform every few
minutes and prints a line when an alert fires. JueX treats the printed line as
an Observation, records it durably, and queues it if the agent is mid-turn.

### Agent Managed Observables

As an agent, before adding a new Observable I can list existing Observables and
avoid duplicates. I can create a new Observable, start or stop it for the
current process, delete it from config, and query recent Observations.

### Web Inspection

As a user, I can open the Observables page from the main navigation, see which
Observables are running, stop or delete an Observable, and inspect recent
Observations with timestamps, severity, kind, content, artifact paths, and
delivery status.

## Product Rules

### Delivery Target

Observations target the active primary session for the current workspace.
Side sessions do not receive Observations.

If no active primary session exists in `serve`, the Observation is recorded but
not delivered until an active primary can be resolved. The Web UI should make
that status visible as `recorded` or `undelivered`, not silently drop it.

### Filters

Filters are optional. Some commands are already purpose-built event sources and
emit only meaningful messages. When no filters are configured, all captured
output is eligible for batching and delivery.

Batching is not optional. Every Observable must have a valid batch policy so
unexpected output volume cannot flood the agent.

### Severity And Kind

Every Observation has:

- `kind`: a stable category such as `log_batch`, `lark_notification`,
  `test_failure`, `process_exit`, or `matched_output`.
- `severity`: `info`, `warning`, `error`, or `critical`.

These values are resolved in this order:

1. Parser mapping from structured output fields, when configured.
2. Matching filter values, when the filter provides kind or severity.
3. Observable defaults.
4. Built-in fallback: `kind=log_batch`, `severity=info`.

Commands do not have to output severity or kind. Configuration can supply them.

### Batching

Each Observable must define:

- `interval_seconds`: minimum 5 seconds, maximum 86400 seconds.
- `max_chars`: maximum 1000 provider-visible characters.

JueX accumulates eligible output during the interval. If the batch exceeds
`max_chars`, JueX writes the full content to an artifact file, sends a bounded
head/tail preview, and includes the artifact path in the Observation.

### Persistence And Reliability

An Observation is persisted before runtime delivery is attempted. The local
record includes a stable id, source Observable id, content preview, artifact
path if present, timestamps, delivery status, target session id if known, and
pending input id if queued.

On restart, JueX can inspect the durable records and avoid re-delivering an
Observation whose stable id is already marked delivered or whose pending input
message id is already present in the session history.

### Runtime Stop And Delete

Stopping an Observable is a runtime action. It stops the process for the
current JueX process, but does not change the config. The Observable starts
again the next time JueX starts.

Deleting an Observable removes it from `.juex/observables.json`, stops the
running process if present, and prevents future startup.

## Web UX

Add an Observables navigation icon before the History icon.

The Observables list page shows:

- Observable id and display name.
- Command summary.
- Status: running, stopped, exited, errored, deleted.
- Streams watched.
- Batch policy summary.
- Last Observation timestamp.
- Recent delivery status.
- Actions: delete, start, stop where valid.

The Observable detail page shows:

- Configuration at the top, formatted as concise key/value sections.
- Runtime status: pid, started at, exited at, exit code, last error, current
  batch window, last delivery.
- Recent Observations below the config.
- Observation rows with timestamp, kind, severity, content preview, artifact
  path, state, and target session.

The UI should be work-focused and compact. It is an operational surface, not a
marketing or teaching page.

## Agent Tool UX

Register Observable tools as builtins, not MCP tools. Tool names:

- `observable_list`
- `observable_create`
- `observable_start`
- `observable_stop`
- `observable_delete`
- `observable_observations`

Tool descriptions must tell the model:

- List existing Observables before creating a new one.
- Avoid creating duplicate or near-duplicate Observables.
- Prefer JSONL parsers for commands that produce structured events.
- Use filters for high-volume commands.
- Deleting is permanent; stopping is temporary.

## Acceptance Criteria

- `.juex/observables.json` can define at least one command-backed Observable.
- JueX starts configured Observables on process startup.
- stdout/stderr are captured and converted into durable Observations.
- Observations are batched with enforced min/max interval and max character
  limits.
- Oversized batches are externalized to artifact files with preview and path.
- Observation delivery reaches the active primary session or pending-input
  queue with stable ids.
- Agent tools can list, create, start, stop, delete, and query Observations.
- Web UI exposes list and detail views and can delete a running Observable.
- Sandbox policy applies when starting Observable commands.
- No raw unbounded stdout is appended to conversation history.
- Unit, e2e, web, and deterministic development-eval checks cover the feature.
