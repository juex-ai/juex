# Observable Controls Design

## Goal

Make the Observable list usable without desktop horizontal scrolling and let a
user manually emit one configured Schedule Observation without changing the
Schedule's active lifecycle.

## Product design

### Compact Observable list

- At tablet and desktop widths (`768px` and wider), the five columns fit inside
  the existing `max-w-5xl` content card without horizontal scrolling.
- Observable, Source, and Last Observation text stay on one line and truncate
  with an ellipsis.
- Hovering or focusing the row link opens a bounded tooltip containing the full
  Observable name, id, source summary, and last Observation content.
- The tooltip wraps long text, has a maximum height, and scrolls internally so
  an unbounded Observation cannot cover the page.
- While the row link is focused, Arrow Up/Down, Page Up/Down, Home, and End
  scroll overflow inside the tooltip.
- The State column is compact.
- The Actions header and cells are sticky on the right. On smaller screens the
  data columns may scroll inside the card, but Actions remain visible.
- Row navigation remains one accessible full-row link. Action buttons stay
  above that link and do not navigate to the detail page.

### Run action

- Schedule rows and Schedule detail pages show `Run`.
- The action uses the `Zap` icon so it is visibly distinct from the `Play`
  icon used by `Start`.
- `Start` activates the Schedule timer lifecycle. `Run` manually emits one
  configured Observation and does not start, stop, or restart the Schedule.
- `Run` works while the Schedule is either running or stopped.
- Command Observables never show `Run`.
- The list and detail page use their existing busy and error feedback.
- The detail action group wraps on narrow screens.

## Non-goals

- No agent-facing tool is added.
- Command Observables cannot be manually run through this API.
- Manual execution does not change the configured timetable, catch-up policy,
  pause state, or schedule cursor.
- This change does not add request idempotency. Each accepted click is a new
  manual trigger.

## Technical design

### Domain boundary

`internal/observable` owns the manual trigger decision. `internal/web` only
maps HTTP to the domain method, and the frontend only mirrors that API.

Three approaches were considered:

1. Build the Observation in the Web handler. This is smaller but leaks Schedule
   defaults, persistence, and delivery policy into a transport.
2. Add `runOnce` to every `sourceRuntime`. This makes the common interface
   shallow because Command sources cannot satisfy the capability honestly.
3. Add `Manager.RunOnce` and a private Schedule-only capability interface.
   This keeps source-specific behavior in the Schedule adapter while preserving
   the shared durable Observation kernel.

Approach 3 is selected.

```go
type runOnceSource interface {
	runOnce(context.Context) (ObservationRecord, error)
}

func (m *Manager) RunOnce(
	ctx context.Context,
	id string,
) (ObservationRecord, error)
```

`Manager.RunOnce` holds `m.mu` across:

1. closed, deleting, unknown-id, and capability checks;
2. durable Observation creation through the adapter;
3. delivery admission.

This serialization prevents `Close` or `Delete` from interleaving between
validation and persistence. The Schedule adapter only calls kernel methods that
do not acquire `m.mu`.

### Manual Observation

The Schedule adapter creates:

```go
ObservationRecord{
	ObservableID:  scheduleID,
	SourceEventID: "schedule:<id>:manual:<random-id>",
	Kind:          resolvedKind(configuredKind),
	Severity:      resolvedSeverity(configuredSeverity),
	WindowStart:   triggeredAt,
	WindowEnd:     triggeredAt,
	Content:       configuredContent,
	Attachments:   configuredAttachments,
	State:         ObservationStateRecorded,
}
```

- `RunID` remains empty because this is not an Observable lifecycle run.
- A cryptographically random identifier makes each accepted trigger distinct,
  including concurrent triggers and tests with a fixed clock.
- The stable `schedule:<id>:` prefix keeps manual records inside existing
  Schedule recovery and deletion cleanup.
- The adapter checks request cancellation before persistence. After persistence
  succeeds, it always submits delivery; later request cancellation cannot turn
  an accepted trigger into an abandoned recorded entry.
- It uses `recordObservation` and `submitDelivery`, so attachment snapshots,
  durable state, events, pending-input admission, delivery tracking, and Close
  waiting remain shared with scheduled occurrences.
- It does not write `schedule_state.jsonl`.

### Errors

`internal/observable` exposes sentinel errors for HTTP classification:

```go
var (
	ErrObservableNotFound  = errors.New("observable: not found")
	ErrManagerClosed       = errors.New("observable: manager closed")
	ErrObservableDeleting  = errors.New("observable: deleting")
	ErrRunOnceUnsupported  = errors.New("observable: run once unsupported")
)
```

Errors wrap the relevant sentinel with the Observable id. Persistence and
delivery-admission failures remain ordinary errors.

### HTTP API

```text
POST /api/observables/{id}/run
Request body: none
Success: 201 Created + ObservationRecord
```

Error mapping:

| Condition | Status | Error code |
| --- | ---: | --- |
| Unknown Observable | `404` | `not_found` |
| Command source, manager closed, or delete in progress | `409` | `conflict` |
| Persistence or unexpected failure | `500` | `general_error` |

The endpoint is Web/API-only and is not registered in the Observable tool
group.

### Frontend API

```ts
export async function runObservable(id: string): Promise<ObservationRecord>
```

Both Observable pages add `"run"` to their local action union and refresh after
the request succeeds.

### List layout

The grid contracts from a fixed `76rem` minimum to approximately `44rem`:

```text
minmax(10rem, 1.15fr)  5.5rem  minmax(10rem, 1fr)
minmax(10rem, 1fr)     8rem
```

The final `8rem` Actions column fits Run, Start/Stop, and Delete icon buttons.
Its header and row cells use `sticky right-0` with an opaque card background.

## Implementation plan

1. Add failing `internal/observable` tests for stopped/running manual triggers,
   uniqueness, unchanged Schedule state, typed rejection, and persistence
   failure.
2. Add the Schedule-only capability, random manual source-event id,
   `Manager.RunOnce`, and make the focused tests pass.
3. Add failing Web e2e coverage, implement `POST /run` with typed error mapping,
   and update stable backend documentation.
4. Add a failing TypeScript client contract test and implement
   `runObservable`.
5. Replace the obsolete list-width tests with compact/sticky/tooltip/Run
   assertions, then update both React pages and Web design documentation.
6. Run focused, deterministic, race, integration, build, development-eval,
   API, and browser verification before delivery.

## Documentation

- Add the Run route and manual-trigger semantics to `ARCHITECTURE.md`.
- Mention the Schedule Run control in `README.md`.
- Add the compact, sticky Observable table behavior to `DESIGN.md`.
- Update `frontend/README.md` page descriptions.

## Test plan

### Observable domain

- A stopped Schedule can run once and deliver the configured Observation.
- A running Schedule can run once without changing its lifecycle status.
- Two triggers at the same injected time create distinct source-event ids and
  durable records.
- Manual records have no `RunID`, use equal window bounds, and keep the
  configured content, kind, severity, and attachments.
- Run once does not create or change Schedule state.
- Command, unknown, closed, and deleting cases return their typed errors.
- A persistence failure does not invoke delivery.
- Manual source-event ids retain the Schedule prefix used by recovery and
  delete cleanup.

### Web API and e2e

- `POST /api/observables/{id}/run` returns `201` and the durable record for a
  stopped Schedule.
- The Observation reaches the existing delivery path.
- Unknown ids return `404`; Command sources return `409`.
- No agent tool definition is added.

### Frontend

- `runObservable` posts to the encoded `/run` route.
- Only Schedule list rows and detail pages render Run.
- Run uses `Zap`, while Start continues to use `Play`.
- The table uses the compact minimum width and sticky Actions column.
- The row tooltip contains untruncated source and Observation values.

### Browser verification

Using a rebuilt binary:

- At desktop width, no horizontal scrollbar is needed and all three Schedule
  actions are visible.
- At phone width, Actions remain pinned while data columns scroll inside the
  card.
- Hovering a truncated row shows wrapped full content.
- Clicking Run creates a new recent Observation without changing the Schedule
  running/stopped state.
- Clicking a row still navigates; clicking an action does not.
