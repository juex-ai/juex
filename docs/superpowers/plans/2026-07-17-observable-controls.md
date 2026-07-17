# Observable Controls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fit Observable controls in the list without desktop horizontal scrolling and add a Web-only Schedule Run action that emits one durable Observation.

**Architecture:** `internal/observable.Manager.RunOnce` selects a private Schedule-only capability while holding the manager lock through durable record creation and delivery admission. The Web route maps typed domain errors to HTTP, and the React pages mirror the route while keeping Start/Stop lifecycle controls separate.

**Tech Stack:** Go standard library, Go tests and httptest, React 19, TypeScript, Tailwind CSS, Radix/shadcn Tooltip, Lucide icons, Node test runner.

---

### Task 1: Prove the Schedule manual-trigger contract

**Files:**
- Modify: `internal/observable/manager_test.go`
- Modify: `internal/observable/manager_internal_test.go`

- [x] **Step 1: Write the failing stopped-Schedule test**

Add a test that creates a future interval Schedule without starting it, calls
the wished-for API, waits for delivery, and asserts the domain contract:

```go
record, err := mgr.RunOnce(context.Background(), spec.ID)
if err != nil {
	t.Fatal(err)
}
if record.RunID != "" ||
	!strings.HasPrefix(record.SourceEventID, "schedule:"+spec.ID+":manual:") ||
	!record.WindowStart.Equal(now) ||
	!record.WindowEnd.Equal(now) {
	t.Fatalf("manual record = %+v", record)
}
status, _ := mgr.StatusByID(spec.ID)
if status.State != observable.RunStateStopped {
	t.Fatalf("state = %q, want stopped", status.State)
}
if _, ok, _ := store.ScheduleState(spec.ID); ok {
	t.Fatal("manual trigger changed schedule state")
}
```

- [x] **Step 2: Write failing running, uniqueness, and error tests**

Cover:

```go
first, _ := mgr.RunOnce(context.Background(), spec.ID)
second, _ := mgr.RunOnce(context.Background(), spec.ID)
if first.SourceEventID == second.SourceEventID || first.ID == second.ID {
	t.Fatalf("manual records collided: %+v %+v", first, second)
}
```

Also assert `errors.Is` for unknown, Command, closed, and deleting cases. Use an
internal-package test to mark one Schedule as deleting without exposing a
test-only production method. Configure a StateDir that is a regular file and
assert a persistence failure leaves the delivery counter at zero.

- [x] **Step 3: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/observable -run 'RunOnce|ManualSchedule' -count=1
```

Expected: compile failure because `Manager.RunOnce` and its typed errors do not
exist.

### Task 2: Implement the Schedule-only domain capability

**Files:**
- Modify: `internal/observable/source.go`
- Modify: `internal/observable/source_schedule.go`
- Modify: `internal/observable/manager.go`
- Modify: `internal/observable/schedule.go`

- [x] **Step 1: Add the narrow capability and typed errors**

Add to `source.go`:

```go
type runOnceSource interface {
	runOnce(context.Context) (ObservationRecord, error)
}
```

Add sentinel errors near Manager:

```go
var (
	ErrObservableNotFound = errors.New("observable: not found")
	ErrManagerClosed = errors.New("observable: manager closed")
	ErrObservableDeleting = errors.New("observable: deleting")
	ErrRunOnceUnsupported = errors.New("observable: run once unsupported")
)
```

- [x] **Step 2: Add a random manual source-event id**

Add:

```go
func scheduleManualSourceEventID(observableID string) (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", fmt.Errorf("schedule manual source event id: %w", err)
	}
	return scheduleSourceEventPrefix(observableID) +
		"manual:" + hex.EncodeToString(suffix[:]), nil
}
```

- [x] **Step 3: Implement adapter persistence and delivery admission**

Implement `scheduleSourceRuntime.runOnce`:

```go
func (s *scheduleSourceRuntime) runOnce(ctx context.Context) (ObservationRecord, error) {
	if err := contextStep(ctx); err != nil {
		return ObservationRecord{}, err
	}
	sourceEventID, err := scheduleManualSourceEventID(s.spec.ID)
	if err != nil {
		return ObservationRecord{}, err
	}
	triggeredAt := normalizeNow(s.kernel.now())
	record, created, err := s.kernel.recordObservation(ObservationRecord{
		ObservableID: s.spec.ID,
		SourceEventID: sourceEventID,
		Kind: resolvedKind(s.spec.Observation.Kind),
		Severity: resolvedSeverity(s.spec.Observation.Severity),
		WindowStart: triggeredAt,
		WindowEnd: triggeredAt,
		Content: s.spec.Observation.Content,
		Attachments: append([]eventmedia.AttachmentRef(nil), s.spec.Observation.Attachments...),
		State: ObservationStateRecorded,
	})
	if err != nil {
		return ObservationRecord{}, err
	}
	if !created {
		return ObservationRecord{}, fmt.Errorf("observable: duplicate manual source event %q", sourceEventID)
	}
	if !s.kernel.submitDelivery(context.Background(), record) {
		return record, fmt.Errorf("observable: delivery admission closed")
	}
	return record, nil
}
```

Do not check the request context after persistence.

- [x] **Step 4: Implement Manager.RunOnce under the manager lock**

```go
func (m *Manager) RunOnce(ctx context.Context, id string) (ObservationRecord, error) {
	if m == nil {
		return ObservationRecord{}, fmt.Errorf("%w: nil manager", ErrManagerClosed)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ObservationRecord{}, fmt.Errorf("%w", ErrManagerClosed)
	}
	if m.deleting[id] {
		return ObservationRecord{}, fmt.Errorf("%w: %q", ErrObservableDeleting, id)
	}
	source, ok := m.sources[id]
	if !ok {
		return ObservationRecord{}, fmt.Errorf("%w: %q", ErrObservableNotFound, id)
	}
	runnable, ok := source.(runOnceSource)
	if !ok {
		return ObservationRecord{}, fmt.Errorf("%w: %q", ErrRunOnceUnsupported, id)
	}
	return runnable.runOnce(ctx)
}
```

- [x] **Step 5: Run the focused tests and verify GREEN**

Run:

```bash
go test ./internal/observable -run 'RunOnce|ManualSchedule' -count=1
```

Expected: PASS.

### Task 3: Add the Web API contract

**Files:**
- Modify: `internal/web/observables.go`
- Modify: `tests/e2e/web_test.go`
- Modify: `ARCHITECTURE.md`
- Modify: `README.md`

- [x] **Step 1: Write the failing API e2e test**

Create a future Schedule through `POST /api/observables`, stop it, and call:

```go
resp, err := http.Post(
	ts.URL+"/api/observables/schedule-e2e/run",
	"application/json",
	nil,
)
```

Assert `201`, decode `observable.ObservationRecord`, verify the manual prefix
and empty `RunID`, wait for delivered state, and confirm the Schedule stays
stopped. Add unknown=`404` and Command=`409` cases.

- [x] **Step 2: Run the API test and verify RED**

Run:

```bash
go test ./tests/e2e -run 'Web_.*RunSchedule' -count=1
```

Expected: FAIL with `405` because the `/run` sub-path is unsupported.

- [x] **Step 3: Add the route and typed error mapping**

Add a helper:

```go
func writeRunOnceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, observable.ErrObservableNotFound):
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, observable.ErrManagerClosed),
		errors.Is(err, observable.ErrObservableDeleting),
		errors.Is(err, observable.ErrRunOnceUnsupported):
		writeErr(w, http.StatusConflict, "conflict", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "general_error", err.Error())
	}
}
```

Add the dispatch case:

```go
case rest == "run" && r.Method == http.MethodPost:
	record, err := mgr.RunOnce(r.Context(), id)
	if err != nil {
		writeRunOnceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, record)
```

- [x] **Step 4: Update stable API documentation**

Add `POST /api/observables/<id>/run` and state that it emits one durable
Schedule Observation without changing lifecycle or cursor state. State
explicitly that the API is not an agent tool.

- [x] **Step 5: Run API and domain tests**

Run:

```bash
go test ./internal/observable ./internal/web ./tests/e2e -run 'Observable|Schedule|RunOnce' -count=1
```

Expected: PASS.

### Task 4: Add the typed frontend API

**Files:**
- Modify: `frontend/src/api.ts`
- Modify: `tests/frontend/observable-api-contract.test.ts`

- [x] **Step 1: Write the failing client test**

Import `runObservable`, stub `fetch`, call:

```ts
await runObservable("schedule/source");
```

Assert:

```ts
assert.equal(requests[0].url, "/api/observables/schedule%2Fsource/run");
assert.equal(requests[0].method, "POST");
```

- [x] **Step 2: Run the client test and verify RED**

Run:

```bash
node --test --experimental-strip-types tests/frontend/observable-api-contract.test.ts
```

Expected: compile failure because `runObservable` is not exported.

- [x] **Step 3: Add the API helper**

```ts
export async function runObservable(id: string): Promise<ObservationRecord> {
  return jsonOrThrow(
    await fetch(`${BASE}/api/observables/${encodeURIComponent(id)}/run`, {
      method: "POST",
    }),
  );
}
```

Import `ObservationRecord` in `frontend/src/api.ts`.

- [x] **Step 4: Run the client test and verify GREEN**

Run the same Node test. Expected: PASS.

### Task 5: Compact the list and add Schedule Run controls

**Files:**
- Modify: `frontend/src/pages/Observables.tsx`
- Modify: `frontend/src/pages/ObservableDetail.tsx`
- Modify: `tests/frontend/observables-page.test.ts`
- Modify: `DESIGN.md`
- Modify: `frontend/README.md`

- [x] **Step 1: Write failing list-source contract tests**

Replace the obsolete `76rem` expectation with assertions for:

```ts
assert.match(observablesPageSource, /min-w-\[44rem\]/);
assert.match(observablesPageSource, /sticky right-0/);
assert.match(observablesPageSource, /TooltipContent/);
assert.match(observablesPageSource, /item\.source_type === "schedule"/);
assert.match(observablesPageSource, /<Zap className="size-3\.5" \/>/);
assert.doesNotMatch(observablesPageSource, /min-w-\[76rem\]/);
```

Read `ObservableDetail.tsx` and assert the Schedule-only Run button there too.

- [x] **Step 2: Run the page test and verify RED**

Run:

```bash
node --test --experimental-strip-types tests/frontend/observables-page.test.ts
```

Expected: FAIL on compact width, sticky Actions, tooltip, and Run assertions.

- [x] **Step 3: Implement the compact sticky grid**

Use:

```ts
const observableGridColumns =
  "grid-cols-[minmax(10rem,1.15fr)_5.5rem_minmax(10rem,1fr)_minmax(10rem,1fr)_8rem]";
const observableGridMinWidth = "min-w-[44rem]";
```

Make the Actions header and cell `sticky right-0`, add opaque matching
backgrounds, and retain the existing z-index above the row link.

- [x] **Step 4: Add the bounded row tooltip**

Wrap the full-row link in a Radix Tooltip. Render full name, id, source summary,
and last content inside:

```tsx
<TooltipContent className="block max-h-64 max-w-md overflow-y-auto whitespace-normal break-words px-3 py-2">
  ...
</TooltipContent>
```

Keep the action cell above the overlay so action clicks neither navigate nor
open the row tooltip.

- [x] **Step 5: Add Schedule-only Run actions**

Add `"run"` to both local action unions. Call `runObservable` when selected.
Render:

```tsx
{item.source_type === "schedule" ? (
  <Button title="Run" aria-label="Run schedule now" ...>
    <Zap className="size-3.5" />
  </Button>
) : null}
```

Use the same `Zap` icon plus visible `Run` text on the detail page.

- [x] **Step 6: Update design and module docs**

Document the compact grid, truncation tooltip, sticky Actions column, and
Schedule Run control. Keep the stable documents concise.

- [x] **Step 7: Run frontend tests, lint, and build**

Run:

```bash
pnpm --dir frontend test
pnpm --dir frontend lint
pnpm --dir frontend build
```

Expected: all commands exit 0.

### Task 6: Full verification and browser smoke

**Files:**
- Modify: Taskline `Dev Notes`
- Modify: Taskline `Test Report`

- [x] **Step 1: Run focused and deterministic verification**

```bash
go test ./internal/observable ./internal/web ./tests/e2e -count=1
make test
make build
go test ./... -race -count=1
make integration
```

Expected: PASS, with any intentionally skipped live integration cases recorded.

- [x] **Step 2: Run development evaluation**

```bash
bash tests/eval/development_eval.sh
```

If provider smoke is unavailable for a configuration reason, retain the report
and document the exact limitation.

- [x] **Step 3: Run rebuilt API and browser smoke**

Start the rebuilt binary bound to `0.0.0.0` in an isolated temporary workspace
with one stopped Schedule and one Command Observable. Verify:

- desktop layout has no table horizontal overflow;
- phone layout keeps Actions visible;
- row hover reveals the bounded full-content tooltip;
- row click navigates;
- Run creates a recent Observation and keeps lifecycle state unchanged;
- Start/Stop and Delete retain their behavior.

- [x] **Step 4: Self-review and documentation check**

Review the diff for dead code, error-string classification, agent-tool changes,
stale API lists, and accidental generated assets. Run `git diff --check`.
