# Schedule Source Split Design

## Goal

Separate command Observables from Schedules at the model-facing, persisted
type, and source-runtime layers while keeping one durable Observation kernel.
The split removes the command/schedule union from `observable_create`, makes
invalid cross-source runtime states unrepresentable, and preserves the shared
lifecycle tools, store, delivery path, events, Web status, and config file.

## Domain language

- An **Observable** is a workspace-local configured source that emits durable
  Observations.
- A **Command Observable** perceives output from a managed process and turns
  parsed, filtered, bounded batches into Observations.
- A **Schedule** is an Observable whose source is a timetable. It emits
  pre-authored Observations without a resident process and owns timetable
  catch-up and pause semantics.
- The **Observation kernel** owns run/Observation persistence, delivery,
  events, source-event idempotency, and the shared Observable lifecycle.

`Schedule` becomes a first-class model-facing creation concept but stays in
the `observable` tool group because list/start/stop/delete/history and the
kernel remain shared. The Runtime catalog therefore gains one Observable tool,
not a ninth builtin group.

## Product contract

### Agent tools

`observable_create` creates command Observables only. Its input is flat:

```json
{
  "id": "lark-events",
  "name": "Lark events",
  "command": "lark-cli",
  "args": ["watch", "--json"],
  "cwd": ".",
  "env": {},
  "streams": ["stdout"],
  "parser": {"type": "jsonl", "content_field": "content"},
  "filters": [],
  "batch": {"interval_seconds": 10, "max_chars": 1000},
  "on_exit": {"notify": "nonzero"},
  "observation": {"kind": "lark_notification", "severity": "info"}
}
```

It contains no `source`, `type`, schedule, content, or static attachment
fields. If a call contains schedule-shaped input, the deterministic error says
to use `schedule_create`.

`schedule_create` is separate and flat:

```json
{
  "id": "weekday-brief",
  "name": "Weekday brief",
  "timezone": "Asia/Shanghai",
  "daily": {
    "times": ["09:00"],
    "weekdays": ["mon", "tue", "wed", "thu", "fri"]
  },
  "catch_up": {"mode": "latest", "max_lateness_minutes": 120},
  "observation": {
    "kind": "heartbeat",
    "severity": "info",
    "content": "Prepare a concise work brief."
  }
}
```

Its routing description explicitly says scheduled and recurring activation
belongs here, not in a polling script or command Observable. It contains no
command, stream, parser, filter, batch, or on-exit fields.

`observable_list`, `observable_start`, `observable_stop`,
`observable_delete`, and `observable_observations` remain source-agnostic.

### Persistence

`.juex/observables.json` keeps one `observables` array and accepts only the
tagged shape:

```json
{
  "id": "lark-events",
  "name": "Lark events",
  "type": "command",
  "command_config": {
    "command": "lark-cli",
    "args": ["watch", "--json"],
    "observation": {"kind": "lark_notification", "severity": "info"}
  }
}
```

```json
{
  "id": "weekday-brief",
  "name": "Weekday brief",
  "type": "schedule",
  "schedule_config": {
    "timezone": "Asia/Shanghai",
    "daily": {"times": ["09:00"]},
    "observation": {"content": "Prepare a work brief."}
  }
}
```

There is no legacy reader or migration. The old top-level command shorthand
and nested `source` union become per-entry `ConfigIssue`s. Each issue preserves
the entry id when possible and includes the rewrite target:
`type` plus `command_config` or `schedule_config`. Valid sibling entries still
load through `LoadConfigLenient`; editing remains blocked until issues are
fixed, matching the existing fail-loud policy.

## Type model

The persisted wire representation and validated domain representation are
separate.

`config.go` owns a private wire DTO containing `type`, `command_config`, and
`schedule_config`. Strict decoding rejects unknown top-level fields and the
old shapes. The exported, validated `Spec` contains id/name plus a sealed
package-private source value created through `NewCommandSpec` or
`NewScheduleSpec`:

```go
type Spec struct {
    ID   string
    Name string
    source sourceConfig
}

type CommandSourceSpec struct {
    Command     string
    Args        []string
    CWD         string
    Env         map[string]string
    Streams     []string
    Parser      *ParserSpec
    Filters     []FilterSpec
    Batch       BatchSpec
    OnExit      OnExitSpec
    Observation CommandObservationSpec
}

type ScheduleSourceSpec struct {
    Timezone    string
    Once        *OnceSchedule
    Daily       *DailySchedule
    Interval    *IntervalSchedule
    CatchUp     CatchUpSpec
    Observation ScheduleObservationSpec
}
```

Accessors return defensive values and a source-type discriminator. Once a
wire entry has decoded, it cannot simultaneously carry command and schedule
fields. Validation therefore retains source-local rules but deletes the
inactive-field rejection, preferred-shape, mirroring, and legacy comparison
families.

The command and schedule observation types are distinct. Command defaults
cannot contain static content/attachments; schedule payloads cannot contain
command-output defaults beyond kind/severity.

`runner`, `pipeline`, and `batcher` receive a command-only runtime projection.
`schedule.go` receives a schedule-only runtime projection. Their algorithms
remain unchanged; only type signatures and field paths move mechanically so
no hidden compatibility mirrors or second metadata truth are introduced.

## Runtime architecture

`internal/observable/source.go` defines package-private source contracts:

```go
type sourceRuntime interface {
    start(context.Context, *observableRun) error
    stop(context.Context, *observableRun, sourceStopReason) (sourceStopResult, error)
    deleteState(context.Context, string) error
    statusSnapshot(ObservableStatus) ObservableStatus
}

type sourceStopResult struct {
    Quiesced bool
}
```

Each `observableRun` retains its resolved adapter. `Manager` also keeps one
adapter per validated spec, so Start, Stop, status projection, Delete, and
Close do not switch on source type. A single factory performs the sealed
source dispatch when a spec enters the manager.

Adapters depend on a narrow kernel port, not `*Manager`. The port exposes
atomic lifecycle operations rather than independent mutation primitives:

- `activateRun` records running state and emits one started event;
- `finishRun` identity-checks the run, removes it, records one terminal state,
  and emits exactly one terminal event;
- `recordObservation` atomically returns `(record, created, error)` and owns
  stable source-event idempotency;
- `recordedObservations` provides the bounded recovery query needed by a
  Schedule without exposing Store;
- `submitDelivery` owns tracked asynchronous delivery and preserves the shared
  durable Observation state machine;
- `now` and `isClosed` provide process policy.

The command adapter owns process start/wait/on-exit and runner dependencies.
The schedule adapter owns startup recovery, occurrence loop, schedule-state
records, pause, and schedule status. Schedule state uses a private store port;
the general manager does not inspect schedule records.

The stop reason distinguishes user Stop, Delete, shutdown, and failed startup.
Manager first makes a provisional CAS claim so a command wait or schedule timer
cannot win concurrently, then the adapter cancels and waits for source
quiescence. The claim does not write a terminal record, status, or event. If
`stop` returns an error with `Quiesced=false`, Manager releases the claim,
keeps the run managed, and returns the error. Source workers that reach a
terminal condition while another provisional claim exists defer their terminal
attempt until the claim resolves; after rollback they retry the ordinary
finish path, so a cancelled Schedule cannot return from `ctx.Done` and strand
the run. A user-stopped Schedule persists its pause cursor before Manager
commits the shared stopped transition. If pause persistence fails, `stop`
returns `Quiesced=true` with the error; Manager then commits one errored
terminal transition and returns the error rather than reporting a false Stop.
Shutdown does not persist a pause baseline, so normal offline catch-up remains
possible. The structured result is the only branch signal; Manager never
infers lifecycle state from error text.

Delete provisionally claims and quiesces a live source with the delete reason,
commits its stopped transition, then calls the adapter's idempotent
`deleteState`. For Schedule this clears its cursor and drops still-recorded
schedule Observations. Only after private cleanup succeeds does Manager save
the config without the entry and remove the in-memory spec/adapter/status.
Cleanup or save errors are returned and never reported as deleted. If config
save fails after private cleanup, the still-configured source remains stopped
with an empty private cursor, a safe and retryable state that cannot orphan a
process or inherit stale schedule state on later recreation.

Adapters and runner never launch unregistered delivery goroutines. Kernel
`submitDelivery` checks admission and increments its delivery wait group while
holding the same lock, then starts the asynchronous shared delivery. Close
first provisionally claims and quiesces every source with the shutdown reason
while delivery admission remains open, allowing command final flushes to join
the tracked group. After all sources are quiescent and can no longer submit,
Close atomically closes admission under the submit lock and then waits for the
delivery group. This prevents Add/Wait races, keeps Schedule delivery inside
Close, and retains command final-flush persist-before-deliver ordering.

Schedule recovery uses the kernel's bounded `recordedObservations` query and
resubmits only recorded entries with its stable source-event prefix. New
occurrences use atomic `recordObservation`; an existing source-event id returns
`created=false`, advances schedule state without redelivery, and never exposes
Store directly to the adapter.

The shared kernel remains in `manager.go`: config/spec registry, run
reservation, lifecycle/status registry, Observation store/delivery, event
emission, source-event idempotency, and the app delivery seam.

## Web and Runtime catalog

The `/api/observables` route paths and frontend status DTOs do not change. POST
body input changes from the old union/legacy shape to the new tagged persisted
`Spec`. The currently unused but exported TypeScript `ObservableCreateRequest`
and `createObservable` client are updated to the same discriminated tagged
union so they do not publish a stale flat contract. List/detail continue using
`ObservableStatus.SourceType` and optional schedule status.

The Runtime tool catalog automatically shows `schedule_create` inside the
Observable group. Builtin count becomes 28 and the Observable group count
becomes 7. Stable architecture/design docs and exact-count tests are updated.

## Error handling

- Old persisted shapes become loud, per-entry `ConfigIssue`s with rewrite
  instructions; valid sibling entries still load.
- A tagged command entry must decode a command config; a tagged schedule entry
  must decode a schedule config. Unknown types fail explicitly.
- Tool calls never silently reinterpret one creation concept as the other.
  Schedule-shaped `observable_create` input points to `schedule_create`.
- Adapter start failures use the kernel's single errored transition. Source
  goroutines cannot publish exited/errored after Stop has requested quiescence.
- Schedule delivery retains source-event idempotency and catch-up semantics.

## Test plan

1. Config/type tests: tagged command/schedule round trips, sealed constructors,
   source-local defaults, strict old-shape issues with id and rewrite hint,
   valid sibling loading, duplicate ids, and stable save output.
2. Command component tests: runner, pipeline, and batcher keep behavior through
   the command-only projection on Unix and Windows compile targets.
3. Schedule domain tests: occurrence algorithms keep their existing cases
   through the schedule-only projection.
4. Runtime adapter tests: command and schedule adapters satisfy the same
   start/stop/status contract; stopped transitions happen after quiescence;
   terminal events/records occur exactly once; Close does not record user Stop.
5. Tool tests: two closed create schemas have no cross-source fields;
   schedule-shaped command input points to `schedule_create`; both handlers
   save the tagged shape; the observable tool group has seven definitions.
6. Token measurement: record old and new create-schema costs and assert neither
   new tool contains the old top-level command/schedule union.
7. Web/e2e: create and deliver one command Observable and one Schedule through
   compiled routes; old config issues remain visible and block edits.
8. Catalog/frontend: builtin total 28, Observable group 7, and catalog tests
   include `schedule_create` without adding a group.
9. Full deterministic, race, integration, build, provider/model smoke, and a
   live routing eval whose scheduled-task prompt must call `schedule_create`
   rather than `observable_create` or a polling shell command.

## Non-goals

- No second manager, config file, Observation store, event family, delivery
  path, or lifecycle tool family.
- No automatic migration or legacy compatibility reader.
- No change to schedule occurrence semantics or command parsing/batching.
- No new Web route or required presentation redesign.
- No guide-skill content from coordinated task `218a266d`.
