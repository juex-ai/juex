# Tool Failure Continuation Policy and Ledger

## Goal

Make tool failures part of the runtime's visible turn state so a model cannot
silently finish immediately after a likely blocking failure. Ordinary tool
failures stay provider-visible as `tool_result` observations, and the runtime
also maintains a per-turn unresolved-failure ledger used during finish
attempts.

## Product Contract

- A non-zero completed `exec_command` or `write_stdin` process is a failed tool
  result, not a successful text result.
- Every failed tool result is classified as one of:
  - `recoverable`
  - `external_blocked`
  - `runtime_fatal`
  - `repeated_stuck`
  - `nonblocking_exploratory`
- The runtime records a failure fingerprint, tool identity, exit code when
  available, output/error preview, related paths from structured tool input,
  latest related-file modification time when available, occurrence count, and
  whether the failure may block the goal.
- A model response with no tool calls triggers normal finish handling only when
  no unresolved blocking failure needs attention, or after the runtime has
  already injected an observation for the same unresolved failure set.
- The continuation observation asks the model to fix, verify, change approach,
  or explicitly explain the blocker. Repeated failures use stronger wording to
  discourage rerunning the same failing action.
- Later successful checks can mark failures `resolved`; successful file writes
  or edits against related paths can mark failures `stale`. Stale/resolved
  failures do not block finish.

## Non-Goals

- No domain-specific correctness scoring.
- No generic max-iteration stop.
- No requirement to split shell stdout and stderr; the existing shell surface is
  combined output, so the ledger stores a bounded output preview.
- No durable cross-session failure database in this task. The authoritative
  durable surface is the session event stream.

## Technical Design

### Tool Layer

Add a shell exit error type in `internal/tools` and make `exec_command` /
`write_stdin` return it when a completed process has a non-zero exit code.
Running sessions still return normally with a `session_id`, and timeout handling
continues to come from the registry timeout path.

### Runtime Layer

Add a per-turn ledger in `internal/runtime`:

- `ToolFailureClassification`
- `ToolFailureStatus`
- `ToolFailureRecord`
- helpers for fingerprinting, classification, related-path extraction,
  staleness detection, and finish-continuation prompt construction.

Update `recordToolBatchLocked` after all parallel tool calls finish:

- failed results are added or counted in the ledger and emit
  `tool.failure.recorded`;
- successful results can mark matching unresolved failures `resolved` or
  `stale`, emitting `tool.failure.resolved` or `tool.failure.stale`.

Update finish handling in `TurnMessageWithID`:

- emit `finish.attempted` as today;
- before stop hooks are allowed to close the turn, ask the ledger whether an
  unresolved blocking failure set needs one continuation;
- if so, enqueue a runtime observation as pending input, emit
  `tool.failure.continued`, and continue the provider loop.

### Events and Observability

Add event payloads for:

- `tool.failure.recorded`
- `tool.failure.resolved`
- `tool.failure.stale`
- `tool.failure.continued`

Teach the observability recorder to summarize these events and write them to
`tools.jsonl` because they use the `tool.*` prefix.

### Documentation

Update architecture/runtime docs to mention the unresolved failure ledger and
the shell non-zero exit behavior. Keep it concise and avoid changelog-style
detail.

## Test Plan

- Unit: shell tools return an error for non-zero completed exit codes while
  preserving output and exit code text.
- Unit: classification maps representative failures to the five categories.
- Unit: a failed tool followed by an early final response receives a runtime
  continuation observation and another provider call.
- Unit: a later successful check marks a failure `resolved`.
- Unit: a later successful file write/edit against a related path marks a
  failure `stale`.
- Unit: repeated fingerprints become `repeated_stuck` and use repeated-failure
  continuation text.
- E2E: scripted provider fails a check, tries to finish, receives the runtime
  observation, fixes the file, verifies, and finishes; conversation and events
  contain the expected tool failure ledger records.
- Regression: a single recoverable failure still does not make `juex run` exit
  with an error.
- Full local verification: focused Go tests, full `go test ./...`, build, lint,
  and development eval.
