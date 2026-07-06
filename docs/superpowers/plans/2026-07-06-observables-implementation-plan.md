# JueX Observables Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build JueX Observables: workspace-local command sources that emit durable, bounded Observations into the active primary session.

**Architecture:** Add a focused `internal/observable` package for config, process lifecycle, parsing, batching, persistence, and management tools. Compose it from `internal/app`, deliver Observations through the existing runtime pending-input path, and expose list/detail/control surfaces through Web and frontend.

**Tech Stack:** Go standard library JSON/process/file APIs, existing JueX shell/sandbox/event/runtime/session modules, React + Vite frontend, Taskline for delivery tracking.

---

## File Structure

- Create `internal/observable/config.go`: JSON config structs, validation, defaults, and file load/save.
- Create `internal/observable/store.go`: append-only run and Observation stores.
- Create `internal/observable/manager.go`: process lifecycle, start/stop/delete orchestration.
- Create `internal/observable/runner.go`: command startup, stdout/stderr readers, shutdown.
- Create `internal/observable/pipeline.go`: parser, filters, severity/kind resolution.
- Create `internal/observable/batcher.go`: batch windows, artifact externalization.
- Create `internal/observable/tools.go`: agent tool registration and handlers.
- Modify `internal/config/config.go` and `internal/config/values.go`: expose Observables config and state paths.
- Modify `internal/app/app.go`: construct manager, start configured Observables, deliver Observations to runtime.
- Modify `internal/app/runtime_status.go`: include Observable status if needed by `/api/runtime`.
- Modify `internal/llm/types.go`: add `MessageKindObservation`.
- Modify `internal/web/server.go`, `internal/web/handlers.go`, and `internal/web/browser_event.go`: endpoints and browser events.
- Modify `frontend/src/api.ts`, `frontend/src/types.ts`, `frontend/src/App.tsx`, and shell navigation components.
- Create `frontend/src/pages/Observables.tsx` and `frontend/src/pages/ObservableDetail.tsx`.
- Update `README.md` and `ARCHITECTURE.md` after behavior exists.

## Task 1: Config Path And JSON Schema

**Files:**
- Create: `internal/observable/config.go`
- Create: `internal/observable/config_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/values.go`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write failing config tests**

Add tests that create `.juex/observables.json` with text and JSONL examples, then assert path resolution, defaults, validation, and formatted save behavior.

```go
func TestLoadConfig_DefaultsStreamsAndValidatesBatch(t *testing.T) {
	dir := t.TempDir()
	body := `{"observables":[{"id":"lark-events","command":"lark-cli","args":["watch","--json"],"batch":{"interval_seconds":10,"max_chars":1000}}]}`
	path := filepath.Join(dir, "observables.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := observable.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	got := cfg.Observables[0]
	if got.ID != "lark-events" || !reflect.DeepEqual(got.Streams, []string{"stdout", "stderr"}) {
		t.Fatalf("spec defaults = %+v", got)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `mise exec -- go test ./internal/observable ./internal/config -run 'Observable|Observables' -count=1`

Expected: compile failure because the `internal/observable` package and config path helpers do not exist.

- [ ] **Step 3: Implement config structs and validation**

Implement `observable.FileConfig`, `Spec`, `Defaults`, `ParserSpec`, `FilterSpec`, `BatchSpec`, and `OnExitSpec`. Implement:

```go
func LoadConfig(path string) (FileConfig, error)
func SaveConfig(path string, cfg FileConfig) error
func ValidateConfig(cfg FileConfig) (FileConfig, error)
```

Validation must reject duplicate ids, missing command, invalid streams, invalid severity, invalid batch interval, invalid `max_chars`, and filters that set neither or both `contains` and `regex`.

- [ ] **Step 4: Wire config paths**

Add methods on config values:

```go
func (c Config) ObservablesConfigPath() string
func (c Config) ObservablesStateDir() string
```

Expected paths:

```text
<WorkDir>/.juex/observables.json
<WorkDir>/.juex/observables
```

- [ ] **Step 5: Verify GREEN**

Run: `mise exec -- go test ./internal/observable ./internal/config -run 'Observable|Observables' -count=1`

Expected: PASS.

## Task 2: Durable Observation Store

**Files:**
- Create: `internal/observable/store.go`
- Create: `internal/observable/store_test.go`

- [ ] **Step 1: Write failing store tests**

Cover append/load latest run records, append/list Observations, state update by id, stable id generation, and artifact path generation.

```go
func TestObservationStore_DeduplicatesStableID(t *testing.T) {
	store := observable.NewStore(t.TempDir(), observable.StoreOptions{Now: fixedNow})
	rec := observable.ObservationRecord{
		ObservableID: "lark-events",
		RunID:        "run-1",
		Kind:         "lark_notification",
		Severity:     "info",
		WindowStart:  fixedNow(),
		WindowEnd:    fixedNow().Add(10 * time.Second),
		Content:      "hello",
	}
	first, err := store.RecordObservation(rec)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.RecordObservation(rec)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID {
		t.Fatalf("ids differ: %s %s", first.ID, second.ID)
	}
}
```

- [ ] **Step 2: Verify RED**

Run: `mise exec -- go test ./internal/observable -run 'Store|Observation' -count=1`

Expected: compile failure before store implementation exists.

- [ ] **Step 3: Implement append-only store**

Create:

```go
type Store struct { /* path, clock, mutex */ }
type RunRecord struct { /* fields from technical design */ }
type ObservationRecord struct { /* fields from technical design */ }

func NewStore(root string, opts StoreOptions) *Store
func (s *Store) AppendRun(record RunRecord) error
func (s *Store) LatestRuns() (map[string]RunRecord, error)
func (s *Store) RecordObservation(record ObservationRecord) (ObservationRecord, error)
func (s *Store) UpdateObservation(id string, update func(ObservationRecord) ObservationRecord) error
func (s *Store) ListObservations(filter ObservationFilter) ([]ObservationRecord, error)
func (s *Store) ArtifactPath(observableID, observationID string) string
```

Use JSONL append for state snapshots. Latest state is the last row for an id.

- [ ] **Step 4: Verify GREEN**

Run: `mise exec -- go test ./internal/observable -run 'Store|Observation' -count=1`

Expected: PASS.

## Task 3: Parser, Filters, And Batcher

**Files:**
- Create: `internal/observable/pipeline.go`
- Create: `internal/observable/batcher.go`
- Create: `internal/observable/pipeline_test.go`
- Create: `internal/observable/batcher_test.go`

- [ ] **Step 1: Write failing pipeline tests**

Test text passthrough with no filters, contains filters, regex filters, JSONL field mapping, severity/kind precedence, and invalid JSONL handling.

```go
func TestPipeline_FilterOverridesDefaults(t *testing.T) {
	spec := observable.Spec{
		ID: "test-watch",
		Defaults: observable.Defaults{Kind: "log_batch", Severity: "info"},
		Filters: []observable.FilterSpec{{Contains: "FAIL", Kind: "test_failure", Severity: "error"}},
	}
	pipe, err := observable.NewPipeline(spec)
	if err != nil {
		t.Fatal(err)
	}
	units := pipe.Accept("stderr", []byte("pkg/foo FAIL\n"))
	if len(units) != 1 || units[0].Kind != "test_failure" || units[0].Severity != "error" {
		t.Fatalf("units = %+v", units)
	}
}
```

- [ ] **Step 2: Write failing batcher tests**

Test interval flush, shutdown flush, max char truncation, artifact write, head/tail preview, and process exit notification.

- [ ] **Step 3: Verify RED**

Run: `mise exec -- go test ./internal/observable -run 'Pipeline|Batch' -count=1`

Expected: compile failure before pipeline and batcher exist.

- [ ] **Step 4: Implement parser/filter pipeline**

Implement parsed units:

```go
type ParsedUnit struct {
	Stream    string
	Content   string
	Kind      string
	Severity  string
	ReceivedAt time.Time
}
```

Text parser decodes UTF-8 and applies output hygiene before returning content.
JSONL parser accepts complete lines and maps configured fields.

- [ ] **Step 5: Implement batcher**

Implement:

```go
type Batcher struct { /* spec, store, artifact writer, clock */ }
func (b *Batcher) Add(unit ParsedUnit) ([]ObservationRecord, error)
func (b *Batcher) Flush(reason string) ([]ObservationRecord, error)
```

The batcher returns persisted Observation records only after the store accepts
them.

- [ ] **Step 6: Verify GREEN**

Run: `mise exec -- go test ./internal/observable -run 'Pipeline|Batch' -count=1`

Expected: PASS.

## Task 4: Observable Manager And Process Runner

**Files:**
- Create: `internal/observable/manager.go`
- Create: `internal/observable/runner.go`
- Create: `internal/observable/manager_test.go`
- Create: `internal/observable/testdata/observable_helper.go`

- [ ] **Step 1: Write failing manager tests**

Use a compiled Go helper process that prints controlled stdout/stderr and waits
for stdin close. Test start, stop, delete, exit record, batch flush, and
sandbox runner invocation.

- [ ] **Step 2: Verify RED**

Run: `mise exec -- go test ./internal/observable -run 'Manager|Runner' -count=1`

Expected: compile failure before manager exists.

- [ ] **Step 3: Implement manager options and lifecycle**

Implement:

```go
type ManagerOptions struct {
	ConfigPath string
	StateDir   string
	WorkDir    string
	Shell      config.ShellProfile
	Sandbox    sandbox.Policy
	SandboxRunner sandbox.Runner
	Bus       *events.Bus
	Deliver   func(context.Context, ObservationRecord) error
}

type Manager struct { /* specs, store, runs, delivery */ }
func NewManager(opts ManagerOptions) (*Manager, error)
func (m *Manager) StartAll(ctx context.Context) error
func (m *Manager) Start(ctx context.Context, id string) error
func (m *Manager) Stop(ctx context.Context, id string) error
func (m *Manager) Delete(ctx context.Context, id string) error
func (m *Manager) Status() StatusSnapshot
func (m *Manager) Close() error
```

- [ ] **Step 4: Implement runner**

Runner responsibilities:

- Expand env and cwd.
- Wrap command with sandbox runner before process start.
- Start process and read stdout/stderr concurrently.
- Send bytes into the pipeline.
- Flush on stop, delete, exit, and shutdown.
- Kill process group on cancellation where platform support exists.

- [ ] **Step 5: Verify GREEN**

Run: `mise exec -- go test ./internal/observable -run 'Manager|Runner' -count=1`

Expected: PASS.

## Task 5: Runtime Delivery And App Composition

**Files:**
- Modify: `internal/llm/types.go`
- Modify: `internal/app/app.go`
- Modify: `internal/app/app_test.go`
- Modify: `internal/runtime/loop_test.go`

- [ ] **Step 1: Write failing delivery tests**

Test that an Observation becomes `llm.Message.Kind == "observation"`, queues
through pending input during active turns, starts a new turn when no turn is
active, and deduplicates by stable pending input id.

- [ ] **Step 2: Verify RED**

Run: `mise exec -- go test ./internal/app ./internal/runtime -run 'Observation|Observable' -count=1`

Expected: failures before Observation delivery is wired.

- [ ] **Step 3: Add canonical message kind**

Add:

```go
const MessageKindObservation = "observation"
```

in `internal/llm/types.go`.

- [ ] **Step 4: Implement app delivery callback**

Add an app method:

```go
func (a *App) HandleObservation(ctx context.Context, record observable.ObservationRecord) error
```

This method mirrors MCP notification delivery: build a JSON text message,
enqueue with stable pending input id while a turn is active, otherwise run a
system-originated `TurnMessage`.

- [ ] **Step 5: Compose Observable manager**

In `app.New`, load `.juex/observables.json`, create the manager, register
Observable tools, and add manager cleanup. Start configured Observables after
session attachment is ready.

- [ ] **Step 6: Verify GREEN**

Run: `mise exec -- go test ./internal/app ./internal/runtime -run 'Observation|Observable|PendingInput' -count=1`

Expected: PASS.

## Task 6: Agent Tools

**Files:**
- Create: `internal/observable/tools.go`
- Modify: `internal/tools/builtin.go`
- Test: `internal/observable/tools_test.go`
- Test: `internal/tools/tools_test.go`

- [ ] **Step 1: Write failing tool tests**

Test tool specs, required list-before-create wording, create validation,
start/stop/delete status transitions, and observations listing.

- [ ] **Step 2: Verify RED**

Run: `mise exec -- go test ./internal/observable ./internal/tools -run 'Observable.*Tool|observable_' -count=1`

Expected: tools are missing.

- [ ] **Step 3: Implement tool registration**

Expose:

```go
func RegisterTools(reg *tools.Registry, manager *Manager) error
```

Tool names:

- `observable_list`
- `observable_create`
- `observable_start`
- `observable_stop`
- `observable_delete`
- `observable_observations`

`observable_create` writes `.juex/observables.json` and starts the Observable
immediately.

- [ ] **Step 4: Verify GREEN**

Run: `mise exec -- go test ./internal/observable ./internal/tools -run 'Observable.*Tool|observable_' -count=1`

Expected: PASS.

## Task 7: Web API And Browser Events

**Files:**
- Modify: `internal/web/server.go`
- Modify: `internal/web/handlers.go`
- Modify: `internal/web/browser_event.go`
- Test: `internal/web/handlers_test.go`
- Test: `internal/web/browser_event_test.go`

- [ ] **Step 1: Write failing Web tests**

Test `GET /api/observables`, `POST /api/observables`, detail, start, stop,
delete, observation listing, and SSE projection for observation events.

- [ ] **Step 2: Verify RED**

Run: `mise exec -- go test ./internal/web -run 'Observable|Observation' -count=1`

Expected: 404 or missing DTO failures.

- [ ] **Step 3: Implement endpoints**

Add handlers listed in the technical design. Use server-level manager
snapshots and keep Web as a transport layer. Do not duplicate active-primary
delivery policy in Web handlers.

- [ ] **Step 4: Verify GREEN**

Run: `mise exec -- go test ./internal/web -run 'Observable|Observation|BrowserEvent' -count=1`

Expected: PASS.

## Task 8: Frontend Observables UI

**Files:**
- Modify: `frontend/src/api.ts`
- Modify: `frontend/src/types.ts`
- Modify: `frontend/src/App.tsx`
- Modify: `frontend/src/components/AppShell.tsx`
- Create: `frontend/src/pages/Observables.tsx`
- Create: `frontend/src/pages/ObservableDetail.tsx`

- [ ] **Step 1: Add API and types**

Add DTOs for Observable list, detail, status, and Observation records. Add
client functions for list/create/detail/start/stop/delete/observations.

- [ ] **Step 2: Add route and nav icon**

Add `/observables` and `/observables/:id`, with the nav entry before History.

- [ ] **Step 3: Build list page**

Render compact rows with id, command, status, last observation, batch policy,
and actions.

- [ ] **Step 4: Build detail page**

Render configuration and runtime state at the top, then recent Observations.
Include artifact path when a record is truncated.

- [ ] **Step 5: Verify frontend**

Run:

```bash
cd frontend
pnpm lint
pnpm build
```

Expected: PASS.

## Task 9: Cross-Package E2E And Docs

**Files:**
- Modify: `tests/e2e/e2e_test.go`
- Modify: `tests/e2e/web_test.go`
- Modify: `README.md`
- Modify: `ARCHITECTURE.md`
- Modify: `juex.yaml.example` only if examples need a pointer to observables config.

- [ ] **Step 1: Add e2e tests**

Add a compiled binary scenario with a fake provider and an Observable helper
that emits one JSONL notification. Assert:

- `.juex/observables/observations.jsonl` has the record.
- Provider receives an Observation message.
- Active turn queues the Observation as pending input.
- Web endpoints expose the running Observable and recent Observation.

- [ ] **Step 2: Update docs**

Document `.juex/observables.json`, `.juex/observables/`, Observation delivery,
agent tools, Web page, and sandbox behavior.

- [ ] **Step 3: Run focused checks**

Run:

```bash
mise exec -- go test ./internal/observable ./internal/app ./internal/runtime ./internal/web -count=1
mise exec -- go test ./tests/e2e -run 'Observable|Observation' -count=1
```

Expected: PASS.

- [ ] **Step 4: Run full checks**

Run:

```bash
mise exec -- go test ./...
mise exec -- make build
mise exec -- make development-eval
```

Expected: PASS or documented live-provider limitation in the Taskline Test
Report.

## Self-Review Checklist

- Product docs define Observable and Observation consistently.
- No YAML support sneaks into the first version.
- No `autostart` or restart policy is implemented in the first version.
- Observations are persisted before delivery.
- Delivery targets active primary only.
- Side sessions cannot receive Observations automatically.
- Sandbox applies before Observable command startup.
- Agent tools cannot edit in place; delete plus create is the edit path.
- Web does not duplicate runtime delivery policy.
- Raw unbounded stdout never reaches conversation history.
