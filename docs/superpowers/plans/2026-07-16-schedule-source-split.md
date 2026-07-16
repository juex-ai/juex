# Schedule Source Split Implementation Plan

> Execute sequentially. The type migration changes most Observable test
> fixtures, so later tasks start only after the preceding task is green.

**Goal:** Split command Observables and Schedules across persisted types,
creation tools, and source runtime adapters while preserving the shared
Observation kernel.

**Architecture:** A strict wire DTO decodes the tagged JSON shape into a sealed
validated `Spec`. Command and schedule consumers receive source-specific
runtime projections. A package-private source adapter interface owns source
lifecycle, while Manager owns CAS terminal claims, run/Observation persistence,
delivery, and events.

**Stack:** Go standard library, existing CloudWeGo/Cobra application,
React/TypeScript frontend tests, Taskline workflow.

---

## Task 1: Migrate to the sealed tagged Spec

**Files:**

- Modify: `internal/observable/config.go`
- Modify: `internal/observable/config_test.go`
- Modify: `internal/observable/runner.go`
- Modify: `internal/observable/pipeline.go`
- Modify: `internal/observable/batcher.go`
- Modify: `internal/observable/schedule.go`
- Modify: affected `internal/observable/*_test.go`
- Modify: `internal/observable/manager.go` only for mechanical source access

### Step 1: Write failing config contract tests

Replace legacy fixtures with constructor-based command/schedule specs and add
tests that prove:

- tagged command and schedule JSON round-trip exactly;
- old top-level command and nested `source` shapes become per-entry
  `ConfigIssue`s containing the entry id and the rewrite target;
- a valid sibling still loads beside an invalid old entry;
- strict tagged decoding rejects wrong config/type combinations and unknown
  top-level fields;
- command and schedule source-local defaults/validation remain intact;
- saved JSON contains only `type` plus the matching `_config` field.

Run:

```bash
go test ./internal/observable -run 'Config|ValidateSpec|SaveConfig' -count=1
```

Expected: compile/test failure until the new constructors and wire shape exist.

### Step 2: Implement the wire/domain separation

In `config.go`:

- define source-specific public config and observation value types;
- keep `Spec.source` package-private and expose `NewCommandSpec`,
  `NewScheduleSpec`, `SourceType`, `CommandConfig`, and `ScheduleConfig`;
- add a private strict wire DTO and custom marshal/unmarshal helpers;
- make `LoadConfigLenient` decode each raw entry independently;
- delete SourceSpec, legacy shorthand fields, preferred/mirror helpers, and
  inactive-field rejection functions;
- deep-copy maps/slices/pointers during validation and access.

### Step 3: Mechanically project source-specific runtime values

- Define private command and schedule runtime projections with observable id.
- Update runner/pipeline/batcher signatures and field access to the command
  projection without changing parsing, batching, process, or delivery logic.
- Update schedule.go signatures and field access to the schedule projection
  without changing occurrence algorithms.
- Update Manager source access just enough to compile while preserving its
  existing switch until Task 3.

### Step 4: Update all Observable fixtures

Use shared test constructors for command and schedule specs. Remove tests for
cross-source inactive fields; replace them with strict wire-shape tests.
Preserve every source-local validation, occurrence, pipeline, batch, runner,
manager, attachment, and store assertion.

### Step 5: Verify and commit

```bash
gofmt -w internal/observable
go test ./internal/observable -count=1
git diff --check
git add internal/observable
git commit -m "refactor: seal observable source specs"
```

---

## Task 2: Split the model-facing creation tools

**Files:**

- Modify: `internal/observable/tools.go`
- Modify: `internal/observable/tools_test.go`
- Modify: `internal/tools/tools_test.go`
- Modify: `internal/app/runtime_status_test.go`
- Modify: `internal/web/runtime_test.go`
- Modify: `tests/e2e/web_test.go`
- Modify: exact-count Runtime catalog docs/tests as found by `rg`

### Step 1: Write failing schema and routing tests

Assert:

- the Observable group contains seven tools including `schedule_create`;
- `observable_create` is flat, command-only, closed, and contains no source,
  type, schedule, content, or static attachment fields;
- `schedule_create` is flat, schedule-only, closed, and contains no command
  pipeline fields;
- both recurrence/filter sub-schemas remain closed and validate one branch;
- schedule-shaped input to `observable_create` returns an error naming
  `schedule_create`;
- each handler persists the tagged config and starts the correct source;
- catalog total is 28 and `schedule_create` stays in Observable.

Run:

```bash
go test ./internal/observable ./internal/tools ./internal/app ./internal/web ./tests/e2e -run 'Tool|Runtime.*Catalog|ScheduleCreate' -count=1
```

Expected: failures for the missing definition and old union schema.

### Step 2: Implement separate input DTOs and schemas

- Keep definition metadata as the single source of truth.
- Construct sealed Specs explicitly instead of unmarshalling tool input into
  persisted Spec.
- Detect legacy/nested or schedule-shaped command calls before decoding and
  return the corrective routing error.
- Keep shared lifecycle tool handlers unchanged.

### Step 3: Measure the schema cost

Use `contextbudget.EstimateToolTokens` in a focused test or diagnostic to
record each new create-tool cost. Assert structural removal of the old union;
avoid brittle tokenizer totals unless the repository already treats them as a
stable contract.

### Step 4: Verify and commit

```bash
gofmt -w internal/observable internal/tools internal/app internal/web tests/e2e
go test ./internal/observable ./internal/tools ./internal/app ./internal/web ./tests/e2e -run 'Tool|Runtime.*Catalog|ScheduleCreate' -count=1
git diff --check
git add internal/observable internal/tools internal/app internal/web tests/e2e docs/superpowers
git commit -m "feat: split observable creation tools"
```

---

## Task 3: Introduce source runtime adapters and atomic lifecycle claims

**Files:**

- Add: `internal/observable/source.go`
- Add: `internal/observable/source_command.go`
- Add: `internal/observable/source_schedule.go`
- Add: `internal/observable/source_test.go`
- Modify: `internal/observable/manager.go`
- Modify: `internal/observable/manager_internal_test.go`
- Modify: `internal/observable/manager_test.go`

### Step 1: Write failing adapter/lifecycle tests

Add internal package tests with controllable fake sources/kernel operations:

- Start resolves/stores one source adapter and Manager Start/Stop/Status no
  longer switch on source type;
- only the current run can activate or finish;
- user Stop CAS-claims terminal ownership before cancellation, waits for done,
  persists schedule pause, then publishes stopped exactly once;
- structured stop results distinguish non-quiesced rollback from quiesced
  pause failure; rollback keeps the run managed and its worker retries the
  ordinary terminal path after claim resolution;
- command exit and schedule timer cannot overwrite a claimed Stop;
- pause failure returns error and produces an errored terminal result rather
  than a false stopped success;
- Delete stops/quiesces successfully before config/state removal and preserves
  config when stop/private cleanup/save fails; Schedule cleanup is idempotent;
- Close quiesces without a user-stop record or schedule pause baseline;
- delivery admission closes and all tracked deliveries drain before Close.
- source-event recording is atomic and schedule recovery uses only the bounded
  kernel query, never Store directly.

Run:

```bash
go test ./internal/observable -run 'Source|Stop|Delete|Close|Terminal|Delivery' -race -count=1
```

Expected: failures until sourceRuntime and CAS transitions exist.

### Step 2: Add the source contracts and factory

Define `sourceRuntime`, `sourceStopReason`, `sourceStopResult{Quiesced bool}`,
the narrow kernel port, provisional-claim resolution, terminal outcome, and
source-specific store ports in `source.go`. Resolve adapters when validated
specs enter Manager; store the adapter on each run. Manager branches only on
the structured stop result, never error text.

### Step 3: Move command lifecycle

Move start/wait/on-exit/start-failure cleanup into `source_command.go`. Preserve
runner behavior and caller-context versus lifetime-context separation. Remove
runner-owned delivery goroutines: runner calls kernel `submitDelivery`, which
owns async tracking. Use kernel activate/finish operations; source code must
not mutate Manager maps.

### Step 4: Move schedule lifecycle

Move startup recovery, loop, emission, state/pause records, private delete
cleanup, and schedule status into `source_schedule.go`. Use atomic kernel
source-event recording and its bounded recovery query, then submit through the
tracked delivery path; do not access the Observation store or launch untracked
delivery goroutines.

### Step 5: Reduce Manager to the shared kernel

- keep reservation/config/run registries, store/delivery/events, and CAS
  transitions;
- route Start/Stop/Delete/Close/Status through stored adapters;
- make Stop/Delete/Close ordering explicit and preserve source lifetime
  contexts;
- remove moved functions and source-type branches.

### Step 6: Verify and commit

```bash
gofmt -w internal/observable
go test ./internal/observable -race -count=1
GOOS=windows GOARCH=amd64 go test -c ./internal/observable -o /tmp/juex-observable-windows.test.exe
git diff --check
git add internal/observable
git commit -m "refactor: isolate observable source runtimes"
```

---

## Task 4: Update cross-context behavior and stable documentation

**Files:**

- Modify: `tests/e2e/web_test.go`
- Modify: `internal/web/observables.go` tests as needed
- Modify: `frontend/src/types.ts`
- Modify: `frontend/src/api.ts`
- Modify: `tests/frontend/` API/type contract tests
- Modify: `.agents/skills/juex-ddd/SKILL.md`
- Modify: `ARCHITECTURE.md`
- Modify: `README.md`
- Modify: Runtime catalog design/spec exact counts
- Modify: module/frontend docs only when current behavior would otherwise be
  misleading

### Step 1: Add/adjust cross-context tests

- POST one tagged command Spec and one tagged Schedule Spec through the Web API
  and observe delivery through the compiled handler path;
- update the exported TypeScript create request to the tagged discriminated
  union and keep `createObservable` aligned with the POST body;
- start/stop/delete both through the shared routes;
- boot with one old-shape and one valid tagged config, assert the valid source
  runs, the invalid entry is visible as errored, and edits are blocked with the
  rewrite hint;
- assert Runtime catalog shows 28 tools and seven Observable tools.

### Step 2: Update stable docs

- add Schedule and Command Observable to the ubiquitous language;
- document tagged persistence, the two creation tools, shared lifecycle,
  adapter/kernel ownership, and no legacy reader;
- replace old examples and remove union/mixed-shape guidance;
- keep historical docs concise and update exact facts that would mislead the
  next worker.

### Step 3: Verify focused behavior and commit

```bash
gofmt -w tests/e2e internal/web
go test ./internal/observable ./internal/web ./tests/e2e -count=1
git diff --check
git add tests/e2e internal/web frontend/src/types.ts frontend/src/api.ts tests/frontend .agents/skills/juex-ddd/SKILL.md ARCHITECTURE.md README.md docs/superpowers
git commit -m "docs: define schedules as observable sources"
```

---

## Task 5: Final validation and delivery

### Step 1: Independent final review

Review the full diff from `main` for config compatibility intent, source
lifecycle races, shutdown/delivery leaks, provider-facing schema size, Web
compatibility, and doc accuracy. Fix every real issue and rerun the affected
tests.

### Step 2: Repository verification

```bash
make test
make build
make integration
go test ./... -race -count=1
pnpm --dir frontend test
pnpm --dir frontend lint
git diff --check
```

### Step 3: Live model validation

Run the standard development evaluation and provider/model sweep. Add a
temporary isolated routing case whose prompt requests a one-shot or recurring
Schedule; inspect the conversation/events and persisted config to prove the
model called `schedule_create`, not `observable_create` or a polling
`exec_command`.

### Step 4: Browser/API smoke

Run the rebuilt server on `0.0.0.0`, inspect the Runtime catalog and
Observables list/detail with command and schedule entries, and confirm source
badges/status/lifecycle controls remain correct.

### Step 5: Taskline and GitHub workflow

Create/update Dev Notes and Test Report, move through Test, push, create/link
the PR, wait for green CI and at least one review, inspect all three comment
surfaces, address/rebut every finding, merge, write Review Report, mark Done,
sync main, and claim the next task until Taskline returns `null`.
