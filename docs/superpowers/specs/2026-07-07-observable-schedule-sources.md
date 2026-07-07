# JueX Observable Schedule Sources

Date: 2026-07-07
Status: Implemented

## Summary

Add a built-in `schedule` source type to JueX Observables so recurring
heartbeats and one-shot reminders use the same perception path as command
output:

```text
Observable -> source adapter -> durable Observation -> active primary session
```

The product decision is to keep "heartbeat" as a use case, not a separate
runtime subsystem. A heartbeat is an Observable whose source is a schedule.
This avoids shipping a companion CLI just to print scheduled stdout, keeps the
agent-facing tools and Web UI unified, and lets future source types reuse the
same Observation persistence and delivery contract.

## Relationship To Existing Work

The command-backed Observables feature is implemented as the base layer:

- `.juex/observables.json`
- `internal/observable`
- durable records under `.juex/observables/`
- delivery through external pending input
- `observable_*` tools
- Observables Web UI

This task evolves that model from command-backed only to source-backed. The
current command shorthand remains valid for compatibility, but the preferred
configuration shape becomes an explicit `source` union:

```json
{
  "id": "lark-events",
  "source": {
    "type": "command",
    "command": "lark-cli",
    "args": ["watch", "--json"]
  }
}
```

The older backlog direction for standalone heartbeat or scheduled session
events should be treated as superseded for implementation strategy. Its useful
product intent remains: time-based events should reach the active work thread
without becoming an invisible detached cron job.

## Product Design

### Product Language

- **Observable**: a configured object JueX can perceive.
- **Observation**: a durable signal emitted by an Observable.
- **Source**: where the Observable obtains signals.
- **Command source**: captures stdout and stderr from a managed command.
- **Schedule source**: emits Observations at configured times without starting
  an external command.
- **Scheduled occurrence**: one due time from a schedule source. It has a
  stable source event id so restart recovery cannot duplicate it.

This preserves the product sentence:

```text
JueX perceives Observables. Observables emit Observations.
```

`source` stays intentionally plain because it is the technical origin of the
Observation, not the product noun.

### User Value

Users can define recurring prompts such as:

- "Every weekday morning, ask the active agent to prepare a work brief."
- "At 18:00, ask the active agent to summarize today's unresolved tasks."
- "In two hours, remind the active agent to check a deployment."
- "Every 30 minutes while JueX is running, ask the active agent to inspect a
  low-frequency external queue."

These should behave like other external signals:

- persisted before delivery
- deduplicated across restart
- delivered to the active primary session
- queued while a turn is already running
- visible in the Observables page and recent Observation history

### Goals

- Add `source.type = "schedule"` under `.juex/observables.json`.
- Support one-shot, daily, and interval schedules in the first version.
- Emit one bounded Observation per scheduled occurrence.
- Persist schedule state and Observation records before delivery.
- Use stable source event ids derived from `observable_id` and `scheduled_at`.
- Support catch-up on JueX startup when the process was offline.
- Expose schedule source status through existing Observable tools and Web UI.
- Keep the delivery target identical to command Observables: active primary
  only, side sessions excluded.
- Make the offline limitation explicit: JueX only emits on time while a JueX
  process is running; otherwise it can catch up on the next startup according
  to policy.

### Non-Goals

- No external CLI wrapper for heartbeats.
- No OS-level `cron`, `launchd`, or systemd integration in this task.
- No hosted scheduler that fires while the user's machine is off.
- No RFC 5545 calendar recurrence language.
- No autonomous hidden agent lane separate from Observations.
- No global user-level Observables.
- No restart policy for command sources beyond existing behavior.
- No new heartbeat-specific agent tools. Use the Observable tools.

## Configuration

### Preferred File Shape

```json
{
  "observables": [
    {
      "id": "weekday-brief",
      "name": "Weekday brief",
      "source": {
        "type": "schedule",
        "timezone": "Asia/Shanghai",
        "daily": {
          "times": ["09:00"],
          "weekdays": ["mon", "tue", "wed", "thu", "fri"]
        },
        "catch_up": {
          "mode": "latest",
          "max_lateness_minutes": 120
        }
      },
      "observation": {
        "kind": "heartbeat",
        "severity": "info",
        "content": "Prepare a concise Chinese work brief for today. Check calendar, tasks, important mail, and current project status. If an important source cannot be checked, say so explicitly."
      }
    }
  ]
}
```

### One-Shot Example

```json
{
  "observables": [
    {
      "id": "check-deploy",
      "source": {
        "type": "schedule",
        "once": {
          "at": "2026-07-07T18:30:00+08:00"
        },
        "catch_up": {
          "mode": "latest",
          "max_lateness_minutes": 60
        }
      },
      "observation": {
        "kind": "reminder",
        "severity": "info",
        "content": "Check whether the deployment finished cleanly and summarize any failures."
      }
    }
  ]
}
```

### Interval Example

```json
{
  "observables": [
    {
      "id": "queue-check",
      "source": {
        "type": "schedule",
        "interval": {
          "every_seconds": 1800
        },
        "catch_up": {
          "mode": "none"
        }
      },
      "observation": {
        "kind": "heartbeat",
        "severity": "info",
        "content": "Check the configured queue and report only actionable changes."
      }
    }
  ]
}
```

### Explicit Command Source Example

The existing command shorthand remains accepted, but new saved configs should
prefer:

```json
{
  "observables": [
    {
      "id": "lark-events",
      "source": {
        "type": "command",
        "command": "lark-cli",
        "args": ["watch", "--json"],
        "streams": ["stdout"],
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
      },
      "observation": {
        "kind": "lark_notification",
        "severity": "info"
      }
    }
  ]
}
```

### Schema Sketch

```go
type FileConfig struct {
	Observables []Spec `json:"observables"`
}

type Spec struct {
	ID          string          `json:"id"`
	Name        string          `json:"name,omitempty"`
	Source      SourceSpec      `json:"source,omitempty"`
	Observation ObservationSpec `json:"observation,omitempty"`

	// Legacy command shorthand. Accepted on read, normalized on save.
	Command  string            `json:"command,omitempty"`
	Args     []string          `json:"args,omitempty"`
	CWD      string            `json:"cwd,omitempty"`
	Env      map[string]string `json:"env,omitempty"`
	Streams  []string          `json:"streams,omitempty"`
	Defaults Defaults          `json:"defaults,omitempty"`
	Parser   *ParserSpec       `json:"parser,omitempty"`
	Filters  []FilterSpec      `json:"filters,omitempty"`
	Batch    BatchSpec         `json:"batch,omitempty"`
	OnExit   OnExitSpec        `json:"on_exit,omitempty"`
}

type SourceSpec struct {
	Type string `json:"type"`

	Command *CommandSourceSpec `json:"command_source,omitempty"`
	Schedule *ScheduleSourceSpec `json:"schedule_source,omitempty"`

	// Flattened command fields may be accepted for readability if the
	// implementation prefers them. Persisted output should be deterministic.
}

type CommandSourceSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	CWD     string            `json:"cwd,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Streams []string          `json:"streams,omitempty"`
	Parser  *ParserSpec       `json:"parser,omitempty"`
	Filters []FilterSpec      `json:"filters,omitempty"`
	Batch   BatchSpec         `json:"batch"`
	OnExit  OnExitSpec        `json:"on_exit,omitempty"`
}

type ScheduleSourceSpec struct {
	Timezone string         `json:"timezone,omitempty"`
	Once     *OnceSchedule  `json:"once,omitempty"`
	Daily    *DailySchedule `json:"daily,omitempty"`
	Interval *IntervalSpec  `json:"interval,omitempty"`
	CatchUp  CatchUpSpec    `json:"catch_up,omitempty"`
}

type OnceSchedule struct {
	At string `json:"at"`
}

type DailySchedule struct {
	Times    []string `json:"times"`
	Weekdays []string `json:"weekdays,omitempty"`
}

type IntervalSpec struct {
	EverySeconds int `json:"every_seconds"`
}

type CatchUpSpec struct {
	Mode               string `json:"mode,omitempty"` // "", "none", "latest"
	MaxLatenessMinutes int    `json:"max_lateness_minutes,omitempty"`
}

type ObservationSpec struct {
	Kind     string `json:"kind,omitempty"`
	Severity string `json:"severity,omitempty"`
	Content  string `json:"content,omitempty"`
}
```

The exact Go structs may be simplified during implementation, but the external
JSON contract should stay readable and stable.

### Validation Rules

- `id` remains required and unique.
- Exactly one source type is active after normalization.
- Legacy command shorthand cannot be mixed with explicit `source`.
- `source.type` accepts `command` and `schedule`.
- Command sources keep the current command, parser, filter, batch, sandbox, and
  output rules.
- Schedule sources require `observation.content`.
- Schedule `observation.content` must be non-empty and at most 1000
  provider-visible characters.
- Schedule sources do not require `batch`; each due occurrence is already a
  bounded Observation.
- Schedule sources must set exactly one of `once`, `daily`, or `interval`.
- `daily.times` use `HH:MM` 24-hour local time.
- `daily.weekdays` accepts `mon`, `tue`, `wed`, `thu`, `fri`, `sat`, `sun`.
  Omitted weekdays means every day.
- `daily` requires a valid IANA `timezone`.
- `once.at` must include a timezone offset or use RFC3339 with zone.
- `interval.every_seconds` must be at least 60 seconds.
- `catch_up.mode` accepts `none` or `latest`.
- `catch_up.max_lateness_minutes` is required for `latest` and must be between
  1 and 1440.
- Schedule `kind` defaults to `heartbeat`; schedule `severity` defaults to
  `info`.

## Runtime Semantics

### Startup

Configured Observables still start when a JueX process starts. For schedule
sources, "start" means registering timers and evaluating catch-up. It does not
spawn a child process.

### Due Occurrences

A schedule source creates a due occurrence with:

- `scheduled_at`: the intended time.
- `observed_at`: the time the JueX process noticed it.
- `source_event_id`: `schedule:<observable_id>:<scheduled_at-rfc3339>`.

The resulting Observation uses:

- `kind` and `severity` from `observation`.
- `content` from `observation.content`.
- `window_start = scheduled_at`.
- `window_end = observed_at`.
- `source_event_id` persisted on the record.

### Stable Deduplication

Command Observations may continue to derive ids from batch content, window, and
run id. Schedule Observations need source-level stability across process
restart, so the Observation id builder should use `source_event_id` when it is
present.

Add a field:

```go
type ObservationRecord struct {
	SourceEventID string `json:"source_event_id,omitempty"`
}
```

For schedule sources, duplicate `source_event_id` should return the existing
Observation record instead of appending a duplicate.

### Schedule State

Persist source state separately from process run state so catch-up can be
reliable:

```text
.juex/observables/schedule_state.jsonl
```

Suggested record:

```go
type ScheduleStateRecord struct {
	ObservableID            string    `json:"observable_id"`
	LastEvaluatedAt         time.Time `json:"last_evaluated_at,omitempty"`
	LastEmittedScheduledAt  time.Time `json:"last_emitted_scheduled_at,omitempty"`
	UpdatedAt               time.Time `json:"updated_at"`
}
```

On first start with no state, the schedule should establish its baseline at
the current time and wait for the next future occurrence. It should not emit
old occurrences from before the Observable existed unless a future config adds
an explicit `start_at`.

### Catch-Up

When JueX starts after being offline:

- `mode = none`: record the new evaluation time and skip missed occurrences.
- `mode = latest`: emit the latest missed occurrence only if it is not older
  than `max_lateness_minutes`.

Catch-up must still persist the Observation before delivery, and stable ids
must prevent duplicate catch-up if startup is retried.

### Delivery

Schedule Observations use the same app/runtime delivery path as command
Observations:

1. Persist Observation.
2. Resolve the active primary session.
3. If a turn is running, enqueue as pending input.
4. If idle, start or append a system-originated external turn according to the
   current Observation delivery policy.
5. Mark the Observation `queued`, `delivered`, or `recorded` with an error.

Side sessions do not receive schedule Observations automatically.

## Technical Design

### Source Interface

Introduce an internal source seam in `internal/observable`. The exact shape can
be adjusted, but the manager should stop assuming every Observable owns a
child process.

```go
type Source interface {
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
	Status() SourceStatus
}

type SourceFactory interface {
	NewSource(spec Spec, opts SourceOptions) (Source, error)
}
```

`CommandSource` wraps the existing runner, pipeline, batcher, and sandbox
flow. `ScheduleSource` wraps timer management, due-time calculation, schedule
state, and direct Observation creation.

### Manager Changes

- Rename internal `observableRun` concepts that are process-specific or make
  them command-source-specific.
- Keep public states stable where possible: `starting`, `running`, `stopped`,
  `exited`, `errored`.
- For schedule sources, `running` means timers are active.
- Add `source_type` to status payloads.
- For command sources, continue exposing command, args, streams, pid, and
  batch.
- For schedule sources, expose schedule summary, next occurrence,
  last emitted occurrence, catch-up mode, and last error.

### Store Changes

- Add `SourceEventID` to `ObservationRecord`.
- Update `BuildObservationID` to prefer `SourceEventID` when set.
- Add schedule-state append/load helpers.
- Keep append-only JSONL semantics.

### Tools

Existing tools remain the surface:

- `observable_list`
- `observable_create`
- `observable_start`
- `observable_stop`
- `observable_delete`
- `observable_observations`

Tool descriptions should say:

- Observables can have `command` or `schedule` sources.
- The agent must list existing Observables before creating a new one.
- For heartbeat/reminder needs, prefer `source.type = "schedule"` instead of
  creating a command wrapper.

### Web UI

Update the Observables list and detail pages:

- Replace the list column "Command" with "Source".
- Show source badges: `command`, `schedule`.
- For schedule rows, show next occurrence and catch-up mode.
- For command rows, show command label and pid if running.
- Detail top section shows the full source config.
- Recent Observations remain shared.

No new top-level navigation is required because Observables already has a Web
entry.

## Implementation Plan

1. Extend config parsing and normalization.
   - Add explicit source schema.
   - Keep legacy command shorthand accepted.
   - Save configs in deterministic preferred shape.
   - Add config tests for command compatibility and schedule validation.

2. Add source abstraction.
   - Extract current command lifecycle into `CommandSource`.
   - Make manager lifecycle source-type aware.
   - Preserve current command behavior and tests.

3. Add schedule calculation service.
   - Pure functions for next due, missed latest due, daily weekdays, interval,
     and one-shot schedules.
   - Use an injected clock in tests.
   - Cover timezone and boundary cases.

4. Add schedule source runtime.
   - Register timers.
   - Persist schedule state.
   - Create durable Observations with `source_event_id`.
   - Deliver through the existing Observation path.

5. Update status, tools, API, and frontend.
   - Add `source_type` and schedule status fields.
   - Update tool schemas and descriptions.
   - Update Web DTOs and list/detail pages.

6. Update docs.
   - README: Observables are source-backed, with command and schedule sources.
   - ARCHITECTURE: `internal/observable` owns source adapters and schedule
     state.
   - Existing Observables docs: mark command-only non-goal as superseded by
     this follow-up.

7. Run verification.
   - Focused Go tests for `internal/observable`, `internal/app`,
     `internal/web`, and `tests/e2e`.
   - Frontend build if UI changes.
   - Development evaluation record if runtime/session/provider-visible
     transcript behavior changes.

## Test Plan

### Unit Tests

- Config accepts legacy command shorthand.
- Config accepts explicit command source.
- Config rejects mixed legacy fields and explicit source.
- Config accepts daily schedule with timezone.
- Config rejects daily schedule without timezone.
- Config rejects two schedule modes in one source.
- Config rejects schedule content over 1000 chars.
- Config validates catch-up mode and lateness bounds.
- Schedule calculator returns next daily occurrence.
- Schedule calculator respects weekdays.
- Schedule calculator handles timezone offsets.
- Schedule calculator returns latest missed occurrence within catch-up window.
- Schedule calculator skips missed occurrence outside catch-up window.
- Interval schedules use first-start baseline and do not backfill old runs.
- `BuildObservationID` is stable for the same `source_event_id`.
- Duplicate schedule occurrence returns existing Observation.
- Schedule state load returns latest record per Observable.

### Manager And Delivery Tests

- `StartAll` starts both command and schedule sources.
- Schedule source reaches `running` without a child process pid.
- Due schedule occurrence records an Observation before delivery.
- Active primary receives schedule Observation.
- Running turn queues schedule Observation as pending input.
- No active primary leaves schedule Observation recorded.
- Restart with catch-up latest emits at most one missed occurrence.
- Restart does not duplicate an already recorded `source_event_id`.
- `Stop` stops timers for schedule source.
- `Delete` removes config and stops schedule source.

### Tool Tests

- `observable_create` can create a schedule source.
- `observable_list` returns source type and schedule status.
- Tool schema examples include schedule source.
- Tool description requires list-before-create.
- `observable_observations` includes schedule Observation records with
  `source_event_id`.

### Web Tests

- `GET /api/observables` returns command and schedule source statuses.
- `GET /api/observables/<id>` returns schedule config and recent
  Observations.
- Observables list labels source type instead of command-only display.
- Detail page renders schedule config, next occurrence, and recent
  Observations.
- Delete stops a schedule source and removes config.

### E2E Tests

- Compiled binary scenario with fake provider:
  1. Create `.juex/observables.json` with a schedule source due soon.
  2. Start JueX.
  3. Schedule source records an Observation.
  4. Provider receives an `observation` message for the active primary.
  5. Observation record is delivered or queued with stable ids.

- Restart scenario:
  1. Record schedule state.
  2. Simulate process offline across one due time.
  3. Restart within catch-up window.
  4. Verify exactly one catch-up Observation.
  5. Restart again and verify no duplicate.

## Acceptance Criteria

- Users can define heartbeat/reminder Observables without an external command.
- Command Observables still work with existing configs.
- New configs can use `source.type = "command"` or `source.type = "schedule"`.
- Schedule Observables persist one durable Observation per due occurrence.
- Stable ids prevent duplicate schedule delivery across restart.
- Catch-up behavior is explicit and covered by tests.
- Active primary receives Observations; side sessions do not.
- Agent tools and Web UI expose source type and schedule status.
- Documentation no longer implies that heartbeat requires a stdout wrapper.
