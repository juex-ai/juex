# JueX Observables Technical Design

Date: 2026-07-06
Status: Proposed

## Overview

Observables add a command-backed external event source to JueX. A configured
Observable starts with the JueX process, captures stdout and stderr, turns
eligible output into durable Observations, and delivers those Observations to
the active primary session through the runtime pending-input path.

The feature should feel parallel to MCP notifications at the application
boundary, but it should not be implemented as an MCP server. Observable command
lifecycle belongs to JueX because it must share workspace shell config,
sandbox policy, process cleanup, local persistence, Web status, and agent
management tools.

## Domain Terms

Add these terms to project docs when implementation lands:

| Term | Meaning | Primary owner |
| --- | --- | --- |
| Observable | A workspace-local command source configured for JueX to observe | `internal/observable`, `internal/config`, `internal/app` |
| Observation | A durable, bounded batch emitted by an Observable and optionally delivered to the active primary session | `internal/observable`, `internal/runtime` |
| Observable run | The runtime process state for one Observable command | `internal/observable` |

## Module Ownership

### `internal/observable`

Owns:

- Config schema for `.juex/observables.json` after config path resolution.
- Validation and defaulting for Observable specs.
- Command process lifecycle and stream capture.
- Parser/filter/batch logic.
- Durable Observation store and recovery helpers.
- Agent-facing tool handlers for Observable management.
- Runtime events emitted by Observables.

Does not own:

- Active primary session selection.
- Runtime turn admission or pending-input queue semantics.
- Provider message formatting beyond producing a canonical Observation message.
- Web HTTP routing or frontend DTOs.

### `internal/app`

Owns:

- Composing the Observable manager with config, sandbox, shell, event bus, and
  active-session delivery callbacks.
- Routing Observations into the active primary session or pending input.
- Exposing Observable status through app services used by Web and tools.

### `internal/runtime`

Owns:

- Accepting Observation messages as external pending input.
- Using the existing pending-input queue and external-event TTL.
- Preserving provider-visible transcript order.

### `internal/web`

Owns:

- Observable REST endpoints.
- SSE/browser event projection.
- Web DTOs and active process observations.

### `frontend/`

Owns:

- Observables navigation entry.
- List page.
- Detail page.
- Delete/start/stop interactions.

## Config File

Use JSON only for the first version:

```text
<WorkDir>/.juex/observables.json
```

JSON is chosen because this config is process-oriented like `.agents/mcp.json`,
is easy for agent tools to rewrite safely, avoids YAML implicit type pitfalls,
and can be parsed with the Go standard library.

### Example: JSONL Notification Source

```json
{
  "observables": [
    {
      "id": "lark-events",
      "command": "lark-cli",
      "args": ["watch", "--json"],
      "streams": ["stdout"],
      "defaults": {
        "kind": "lark_notification",
        "severity": "info"
      },
      "parser": {
        "type": "jsonl",
        "content_field": "content",
        "kind_field": "type",
        "severity_field": "level"
      },
      "batch": {
        "interval_seconds": 10,
        "max_chars": 1000
      }
    }
  ]
}
```

### Example: Text Filter Source

```json
{
  "observables": [
    {
      "id": "test-watch",
      "command": "go",
      "args": ["test", "./...", "-count=1"],
      "streams": ["stdout", "stderr"],
      "filters": [
        {
          "contains": "FAIL",
          "kind": "test_failure",
          "severity": "error"
        },
        {
          "regex": "panic:",
          "kind": "panic",
          "severity": "critical"
        }
      ],
      "batch": {
        "interval_seconds": 10,
        "max_chars": 1000
      }
    }
  ]
}
```

### Schema

```go
type FileConfig struct {
	Observables []Spec `json:"observables"`
}

type Spec struct {
	ID       string            `json:"id"`
	Name     string            `json:"name,omitempty"`
	Command  string            `json:"command"`
	Args     []string          `json:"args,omitempty"`
	CWD      string            `json:"cwd,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Streams  []string          `json:"streams,omitempty"`
	Defaults Defaults          `json:"defaults,omitempty"`
	Parser   *ParserSpec       `json:"parser,omitempty"`
	Filters  []FilterSpec      `json:"filters,omitempty"`
	Batch    BatchSpec         `json:"batch"`
	OnExit   OnExitSpec        `json:"on_exit,omitempty"`
}

type Defaults struct {
	Kind     string `json:"kind,omitempty"`
	Severity string `json:"severity,omitempty"`
}

type ParserSpec struct {
	Type          string `json:"type"`
	ContentField  string `json:"content_field,omitempty"`
	KindField     string `json:"kind_field,omitempty"`
	SeverityField string `json:"severity_field,omitempty"`
	TimeField     string `json:"time_field,omitempty"`
}

type FilterSpec struct {
	Contains string `json:"contains,omitempty"`
	Regex    string `json:"regex,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Severity string `json:"severity,omitempty"`
}

type BatchSpec struct {
	IntervalSeconds int `json:"interval_seconds"`
	MaxChars        int `json:"max_chars"`
}

type OnExitSpec struct {
	Notify string `json:"notify,omitempty"` // "", "never", "always", "nonzero"
}
```

Validation rules:

- `id` is required, unique, and uses lower-case letters, digits, `_`, and `-`.
- `command` is required.
- `streams` defaults to `["stdout", "stderr"]`.
- `streams` accepts only `stdout` and `stderr`.
- `batch.interval_seconds` is required and clamped/rejected outside 5 to
  86400 seconds.
- `batch.max_chars` is required and must be 1 to 1000.
- `defaults.severity` and filter/parser severity values must resolve to
  `info`, `warning`, `error`, or `critical`.
- `parser.type` accepts `text` or `jsonl` in the first version.
- A filter must set exactly one of `contains` or `regex`.
- Regex filters are compiled at config load and fail startup softly for that
  Observable, not for the whole process.
- Environment expansion supports the same variables as MCP command startup:
  `${WORKDIR}`, `$WORKDIR`, `${JUEX_WORKDIR}`, and `$JUEX_WORKDIR`.

## Runtime Files

```text
.juex/
├── observables.json
└── observables/
    ├── runs.jsonl
    ├── observations.jsonl
    └── artifacts/
        └── <observable-id>/
            └── <observation-id>.txt
```

`runs.jsonl` records process state snapshots:

```go
type RunRecord struct {
	ObservableID string    `json:"observable_id"`
	RunID        string    `json:"run_id"`
	State        string    `json:"state"` // starting, running, stopped, exited, errored
	PID          int       `json:"pid,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	ExitedAt     time.Time `json:"exited_at,omitempty"`
	ExitCode     *int      `json:"exit_code,omitempty"`
	Error         string    `json:"error,omitempty"`
}
```

`observations.jsonl` records durable Observation states:

```go
type ObservationRecord struct {
	ID             string    `json:"id"`
	ObservableID   string    `json:"observable_id"`
	RunID          string    `json:"run_id,omitempty"`
	Kind           string    `json:"kind"`
	Severity       string    `json:"severity"`
	Stream         string    `json:"stream,omitempty"`
	WindowStart    time.Time `json:"window_start"`
	WindowEnd      time.Time `json:"window_end"`
	Content        string    `json:"content"`
	OriginalChars  int       `json:"original_chars"`
	Truncated      bool      `json:"truncated,omitempty"`
	ArtifactPath   string    `json:"artifact_path,omitempty"`
	State          string    `json:"state"` // recorded, queued, delivered, dropped
	TargetSession  string    `json:"target_session,omitempty"`
	PendingInputID string    `json:"pending_input_id,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	DeliveredAt    time.Time `json:"delivered_at,omitempty"`
	Error           string    `json:"error,omitempty"`
}
```

Observation ids are stable:

```text
obs-<hex sha256(observable_id + run_id + window_start + window_end + kind + severity + content_hash)[:16]>
```

Pending input ids use the Observation id:

```text
observation-<observation-id>
```

## Process Lifecycle

The Observable manager starts when a JueX process starts:

- CLI `run` and `repl`: start Observables for the single primary app.
- Web `serve`: start Observables after the listener is available and after the
  active primary session record can be resolved.

Every configured Observable starts automatically. There is no `autostart` flag
in the first version.

Runtime stop:

- Stops the process and records a stopped run state.
- Does not edit `.juex/observables.json`.
- The Observable starts again on the next JueX process startup.

Delete:

- Stops the process if running.
- Removes the spec from `.juex/observables.json`.
- Records a deleted/stop event.
- Keeps historical Observations on disk for audit and UI history.

Restart policy is intentionally deferred. If a process exits, it stays exited.
Optional `on_exit.notify` can emit an Observation.

## Command Execution

Observable command startup should reuse the same platform assumptions as shell
tools:

- Resolve the workspace shell profile from `config.ShellProfile`.
- Apply sandbox policy before command startup when sandbox is enabled.
- Set `WORKDIR` and `JUEX_WORKDIR`.
- Apply configured `cwd`, defaulting to WorkDir.
- Apply configured `env` after variable expansion.
- Capture stdout and stderr separately but allow batching to merge by
  Observable and batch window.

Implementation can share helper functions with shell tools where safe, but the
manager must not reuse `exec_command` sessions because Observables need
different lifecycle, persistence, and delivery semantics.

## Parsing, Filtering, And Batching

Pipeline:

```text
process bytes -> stream decoder -> parser -> filters -> batcher -> store -> deliver
```

### Text Parser

The default parser treats decoded UTF-8 text as content. Binary or binary-like
data goes through the same output hygiene policy used by tools before it can
reach runtime events, Web UI, artifacts, or provider-visible text.

### JSONL Parser

The JSONL parser reads one JSON object per line. Invalid JSON lines become
`kind=parse_error`, `severity=warning` Observations only when a config flag is
later added; in the first version they should be recorded as runtime errors and
not delivered to the agent.

Mapped fields:

- `content_field` becomes Observation content.
- `kind_field` becomes Observation kind.
- `severity_field` becomes Observation severity.
- `time_field` can be recorded as source timestamp, but window time still uses
  local receive time.

### Filters

Filters are optional. If filters exist, only matching parsed units are eligible
for delivery. If no filters exist, every parsed unit is eligible.

Filter kind/severity overrides parser/default values for matching content.

### Batcher

Each Observable has one active batch window. The window flushes when:

- `interval_seconds` elapses and content exists.
- A process exit notification needs to be emitted.
- The process is stopped or deleted and content exists.
- JueX is shutting down and content exists.

If accumulated content exceeds `max_chars`, write the full content to
`.juex/observables/artifacts/<observable-id>/<observation-id>.txt`, set
`truncated=true`, and use a head/tail preview in `content`.

## Delivery To Runtime

Add a canonical Observation message:

```go
const MessageKindObservation = "observation"
```

Observation message text is JSON, not prose:

```json
{
  "kind": "observation",
  "observation_id": "obs-...",
  "observable_id": "lark-events",
  "severity": "info",
  "observation_kind": "lark_notification",
  "window_start": "2026-07-06T10:00:00Z",
  "window_end": "2026-07-06T10:00:10Z",
  "content": "bounded content",
  "truncated": false,
  "artifact_path": ""
}
```

`internal/app` receives persisted Observation records from the manager and:

1. Resolves active primary session.
2. Builds an `llm.Message` with `Kind=MessageKindObservation`.
3. Calls `Engine.EnqueuePendingMessageWithOptions` with stable
   `PendingInputOptions.ID`.
4. If there is no active turn, calls `Engine.TurnMessage`.
5. Updates the Observation record state to `queued` or `delivered`.

If delivery fails for a transient reason, leave the record `recorded` with an
error. Recovery logic can retry recorded Observations on next startup.

## Runtime Events

Emit events for observability and Web projection:

- `observable.started`
- `observable.stopped`
- `observable.exited`
- `observable.errored`
- `observation.recorded`
- `observation.queued`
- `observation.delivered`
- `observation.dropped`

Payloads should be structured and bounded. They must never include unbounded
raw output.

## Agent Tools

Register tools through `internal/tools` from the Observable manager:

- `observable_list`
- `observable_create`
- `observable_start`
- `observable_stop`
- `observable_delete`
- `observable_observations`

The manager writes `.juex/observables.json` for create/delete. Writes should
be atomic: marshal formatted JSON to a temporary file in `.juex/`, fsync when
reasonable, and rename.

`observable_create` input should be close to the JSON spec. The tool
description must instruct the model to call `observable_list` first and avoid
near duplicates.

## Web API

Add endpoints:

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/api/observables` | list specs plus runtime status |
| `POST` | `/api/observables` | create and start an Observable |
| `GET` | `/api/observables/<id>` | spec, status, recent Observations |
| `POST` | `/api/observables/<id>/start` | start a stopped/exited Observable |
| `POST` | `/api/observables/<id>/stop` | stop a running Observable |
| `DELETE` | `/api/observables/<id>` | delete spec and stop process |
| `GET` | `/api/observables/<id>/observations` | paginated Observation history |

## Frontend

Add an Observables route and navigation item before History.

Files likely touched:

- `frontend/src/App.tsx`
- `frontend/src/api.ts`
- `frontend/src/types.ts`
- `frontend/src/pages/Observables.tsx`
- `frontend/src/pages/ObservableDetail.tsx`
- `frontend/src/components/AppShell.tsx`
- `frontend/src/lib/runtime-display.ts` if shared status labels are useful.

The UI should mirror server DTOs. It should not infer delivery rules or hidden
runtime policy on the client.

## Compatibility And Migration

No migration is needed. If `.juex/observables.json` does not exist, JueX starts
with no Observables.

Older sessions do not contain Observation messages. Existing history loading
should continue to treat unknown `Message.Kind` values as ordinary messages
unless provider projection needs special handling.

## Failure Modes

- Bad config: mark affected Observable as errored in runtime status; do not
  crash the whole JueX process.
- Bad regex: same as bad config for that Observable.
- Command startup failure: record run state `errored`, emit
  `observable.errored`, expose in Web and tool list.
- Command exits: record `exited`; emit an exit Observation only when
  configured.
- Artifact write failure: emit `observation.dropped` with error; do not deliver
  a truncated Observation that claims an artifact exists.
- Pending input queue full: keep Observation `recorded` with delivery error so
  it can be retried or inspected.
- No active primary: keep Observation `recorded`; Web status should explain
  that no active primary target was available.

## Security And Trust

Observables are workspace-local in the first version. They are not loaded from
user-global extensions. Because Observables auto-start commands, this avoids a
hidden cross-workspace execution surface.

Sandbox applies at command startup. If sandbox is enabled and the backend
cannot enforce policy, Observable startup fails closed just like sandboxed
shell command startup.

Agent-created Observables modify workspace-local `.juex/observables.json`.
Tool descriptions should make permanence clear.
