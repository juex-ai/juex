# JueX Observables Test Plan

Date: 2026-07-06
Status: Proposed

## Scope

This test plan covers the first implementation of JueX Observables:

- JSON config loading from `.juex/observables.json`.
- Workspace-local Observable command startup.
- stdout/stderr capture.
- parser, filter, and batch behavior.
- durable Observation persistence.
- runtime delivery to the active primary session and pending-input queue.
- agent management tools.
- Web API and frontend UI.
- sandbox and output hygiene behavior.

## Test Strategy

Use layered tests that match JueX module ownership:

- `internal/observable`: pure config, store, parser, batcher, manager, and
  process-runner tests with fake helpers.
- `internal/app` and `internal/runtime`: delivery, pending input, active
  primary, and restart behavior.
- `internal/web`: HTTP handlers and browser events.
- `frontend`: build and UI route checks.
- `tests/e2e`: compiled-binary behavior with fake provider and real local
  Observable helper commands.
- `tests/eval`: deterministic development evidence after implementation.

Live provider tests are not required for the Observable core because the main
behavior is provider-independent. `make development-eval` should still run
before completion because Observations affect provider-visible transcript shape.

## Unit Test Matrix

### Config

Package: `internal/observable`

Cases:

- missing config file returns empty config.
- valid config loads one Observable.
- `streams` defaults to stdout and stderr.
- duplicate ids fail validation.
- missing command fails validation.
- invalid id fails validation.
- invalid stream fails validation.
- invalid severity fails validation.
- invalid batch interval below 5 seconds fails validation.
- invalid batch interval above 86400 seconds fails validation.
- invalid `max_chars` above 1000 fails validation.
- filter with both `contains` and `regex` fails validation.
- filter with neither `contains` nor `regex` fails validation.
- JSON save writes stable formatted output.
- variable expansion handles `${WORKDIR}` and `$WORKDIR`.

Command:

```bash
mise exec -- go test ./internal/observable -run Config -count=1
```

### Store

Package: `internal/observable`

Cases:

- append and load latest run record.
- run state updates by appending later records.
- record Observation assigns stable id.
- duplicate Observation content returns same stable id.
- list Observations by Observable id.
- list Observations by limit.
- update Observation state to queued.
- update Observation state to delivered.
- unknown Observation update is a no-op or explicit not-found error, whichever
  the implementation chooses and documents.
- artifact path is inside `.juex/observables/artifacts/<observable-id>/`.
- malformed JSONL returns a clear parse error.

Command:

```bash
mise exec -- go test ./internal/observable -run 'Store|Observation' -count=1
```

### Parser And Filters

Package: `internal/observable`

Cases:

- text parser emits content when filters are absent.
- text parser drops nonmatching content when filters exist.
- contains filter matches.
- regex filter matches.
- regex compile failure fails validation.
- filter kind and severity override defaults.
- JSONL parser maps content/kind/severity fields.
- JSONL parser uses defaults when fields are missing.
- JSONL parser rejects invalid line without delivering to runtime.
- binary-like input is sanitized before becoming content.

Command:

```bash
mise exec -- go test ./internal/observable -run 'Pipeline|Parser|Filter' -count=1
```

### Batcher

Package: `internal/observable`

Cases:

- flushes after configured interval.
- does not flush empty batches.
- flushes on process exit.
- flushes on manager stop.
- flushes on manager close.
- writes artifact when content exceeds `max_chars`.
- preview includes bounded head and tail.
- Observation stores original char count and `truncated=true`.
- artifact write failure prevents delivery.
- multiple streams in one window produce deterministic content order.

Command:

```bash
mise exec -- go test ./internal/observable -run Batch -count=1
```

### Manager And Runner

Package: `internal/observable`

Use a compiled helper command from testdata. The helper should support:

- print once and exit zero.
- print stderr and exit nonzero.
- wait and print after a delay.
- print large output.
- print JSONL.
- ignore stdin until killed.

Cases:

- `StartAll` starts every spec.
- `Start` starts a stopped Observable.
- `Stop` stops a running Observable.
- `Delete` stops process and removes config entry.
- command exit records `exited`.
- startup error records `errored`.
- stdout is captured.
- stderr is captured.
- configured streams are honored.
- sandbox runner is invoked before process start when sandbox enabled.
- manager close terminates children.

Command:

```bash
mise exec -- go test ./internal/observable -run 'Manager|Runner' -count=1
```

## Runtime And App Tests

Packages: `internal/app`, `internal/runtime`

Cases:

- Observation message uses `llm.MessageKindObservation`.
- Observation message body is JSON with observation id, Observable id, kind,
  severity, window, content, truncation, and artifact path.
- no active turn: delivery starts a `TurnMessage`.
- active turn: delivery queues pending input.
- pending input id is stable and derived from Observation id.
- duplicate Observation does not enqueue twice.
- pending queue full leaves Observation recorded with error.
- active primary session receives Observation.
- side session does not receive Observation.
- no active primary leaves Observation recorded.
- restart recovery does not re-deliver already delivered Observation.
- restart recovery can retry recorded undelivered Observation.

Command:

```bash
mise exec -- go test ./internal/app ./internal/runtime -run 'Observable|Observation|PendingInput' -count=1
```

## Agent Tool Tests

Packages: `internal/observable`, `internal/tools`

Cases:

- tool specs are registered.
- `observable_list` returns config and runtime state.
- `observable_create` validates input and writes config.
- `observable_create` starts the Observable immediately.
- `observable_create` rejects duplicate id.
- tool description instructs the model to list before creating.
- `observable_start` starts stopped/exited Observable.
- `observable_stop` stops running Observable.
- `observable_delete` stops and removes config.
- `observable_observations` returns recent records with truncation metadata.
- all tool outputs are bounded and structured.

Command:

```bash
mise exec -- go test ./internal/observable ./internal/tools -run 'Observable.*Tool|observable_' -count=1
```

## Web Backend Tests

Package: `internal/web`

Cases:

- `GET /api/observables` returns empty list when config absent.
- `GET /api/observables` returns config plus runtime status.
- `POST /api/observables` creates and starts a new Observable.
- `GET /api/observables/<id>` returns detail and recent Observations.
- `POST /api/observables/<id>/start` starts stopped Observable.
- `POST /api/observables/<id>/stop` stops running Observable.
- `DELETE /api/observables/<id>` stops and deletes.
- `GET /api/observables/<id>/observations` supports limit.
- Observable runtime events are projected to browser events.
- handler errors are stable JSON API errors.

Command:

```bash
mise exec -- go test ./internal/web -run 'Observable|Observation|BrowserEvent' -count=1
```

## Frontend Tests

Commands:

```bash
cd frontend
pnpm lint
pnpm build
```

Manual or Playwright-assisted checks:

- Observables nav item appears before History.
- list page renders empty state.
- list page renders running Observable row.
- detail page renders config and status.
- recent Observation row shows kind, severity, content, state, and artifact.
- delete action removes row and stops process.
- long content wraps without layout overlap.
- mobile viewport keeps controls readable.

If the repo has no existing Playwright harness for this UI, document manual
browser verification in the Taskline Test Report.

## E2E Tests

Package: `tests/e2e`

### Compiled CLI Scenario

Fake provider script:

1. Waits for user prompt.
2. Receives Observation message from Observable delivery.
3. Replies with a final text acknowledging the Observation.

Observable helper:

```bash
observable-helper --jsonl-once '{"type":"lark_notification","level":"info","content":"hello from observable"}'
```

Assertions:

- `observations.jsonl` exists.
- record id is stable.
- record state becomes delivered.
- provider request contains `MessageKindObservation`.
- conversation history contains the Observation message.
- events contain `observable.started` and `observation.delivered`.

### Pending Input Scenario

Fake provider starts a long tool call. While the turn is active, helper emits
an Observation.

Assertions:

- Observation record state becomes queued.
- `pending_input.jsonl` contains stable pending input id.
- queued Observation drains before the next provider request.

### Web Scenario

Start `juex serve` with an Observable helper.

Assertions:

- `/api/observables` shows the Observable.
- `/api/observables/<id>/observations` shows emitted Observation.
- delete endpoint stops the helper process.

Command:

```bash
mise exec -- go test ./tests/e2e -run 'Observable|Observation' -count=1
```

## Full Verification

Run before marking implementation complete:

```bash
mise exec -- go test ./internal/observable ./internal/app ./internal/runtime ./internal/web -count=1
mise exec -- go test ./tests/e2e -run 'Observable|Observation' -count=1
mise exec -- go test ./...
mise exec -- make build
mise exec -- make development-eval
```

If `make development-eval` requires local provider credentials that are
unavailable, record the exact failure in the Taskline Test Report and run all
deterministic tests instead. Do not call the feature complete without that
explicit limitation.

## Regression Risks

- Provider-visible history may become invalid if Observation messages are
  inserted during tool result adjacency. Pending input must drain only at safe
  boundaries.
- Long-running Observable processes may survive shutdown if process groups are
  not cleaned up.
- Agent tools may create duplicate Observables unless list-before-create
  guidance is enforced in descriptions and tested.
- Web may show stale process status if manager snapshots are not synchronized.
- JSON config writes may corrupt config on interrupted writes unless they use
  temp file plus rename.
- Unbounded stdout may leak into provider context if batch/artifact limits are
  bypassed.

## Exit Criteria

- All focused unit tests pass.
- E2E Observation delivery and pending-input scenarios pass.
- Frontend builds.
- Documentation is updated.
- Taskline Spec, Dev Notes, Test Report, and Review Report are attached during
  execution.
- PR review and CI are complete before marking the implementation task done.
