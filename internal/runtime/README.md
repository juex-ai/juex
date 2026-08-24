# Runtime Lifecycle

This package owns Juex's Framework-level Turn loop. The stable product meaning
is defined in [`DOMAIN.md`](../../DOMAIN.md); the repository boundary and data
flow are defined in [`ARCHITECTURE.md`](../../ARCHITECTURE.md). This file is the
implementation-facing checkpoint map, failure matrix, and test index. It does
not define a separate lifecycle.

## Ownership And Checkpoints

`internal/app` classifies transport input and owns active Session replacement.
`internal/runtime` owns Turn admission after that classification, the durable
Pending-input queue, Provider iteration ordering, Tool execution ordering,
typed policy orchestration, and the final completion check. `internal/session`
owns the transcript, Event journal, and single-writer Session lock.
`internal/events` and `internal/eventcatalog` provide the commit-before-project
boundary. `internal/tools` owns raw handler execution, while
`internal/toolevents` owns stable Tool Event payload constructors.
`internal/session` owns the per-call recovery-state projection and the pure
projection from transcript repairs to recovery Events; Runtime and Session load
commit that same repair projection through their own durable Event paths.

The required order is:

1. **Turn input:** append a non-replayable `accepting` intent for a new main
   input; commit `turn.admitted`; promote the record to `admitted`; repair the
   transcript; apply ordered Turn-input policies; project and append the
   accepted message; only then checkpoint and call a Provider. A failed
   admission commit attempts to mark a new intent `dropped`; if that
   compensation also fails, the surviving `accepting` intent remains inert
   unless restart finds the matching committed admission Event, and both errors
   are returned.
2. **Pending input:** append `pending` before adding the live queue entry; drain
   only at a Provider-iteration boundary; mark `admitted`, project and append in
   queue order, then mark `processed`. `pending_input.jsonl` remains authoritative
   across cancellation, Turn boundaries, and restart.
3. **Tool batch:** treat one or more Tool Calls from one Provider response as
   the same ordered batch. Commit `llm.responded` and every ordered
   `tool.requested` before any call starts. Commit `tool.running` before the
   first pre-Tool policy or handler action. Checkpoint each transformed input before a later policy can
   observe it. After raw handler output and post-Tool policy, project the complete
   ordered result batch; commit one terminal Tool outcome containing each exact
   Provider-visible block; append that same batch before the next Provider call.
4. **Finish:** commit `llm.responded`; evaluate every Finish Policy; queue all
   valid policy context; commit the first still-valid continuation candidate;
   durably enqueue its continuation; notify its observation-only callback; let
   the Pending-input queue make the final continue-or-complete decision; then
   commit `turn.completed` or, on failure, `turn.errored`.
5. **Session replacement:** `internal/app` creates a candidate and provisionally
   updates persisted active history before attempting its lock. Lock failure may
   leave the candidate selected and requires history reconciliation. After the
   lock succeeds, App builds, validates, and starts the complete candidate
   Module set and Tool registry; captures the old Engine checkpoint; publishes
   the candidate under the App lifecycle writer; runs Session-start policy;
   commits App/status state; releases readers; then closes the old Module set,
   lock, and Session. Later rejection deletes the candidate and reasserts the
   resident old App Session in active history after runtime rollback. That write
   is not a compare-and-swap restore of any concurrent history selection.

The lock order for replacement and Turn work is App Session lifecycle,
`Engine.mu`, `Engine.sessionRuntimeMu`, `Engine.pendingMu`, then Session or
Module-owned store locks. Do not acquire these in reverse order.

Tool handler output is not automatically the Provider-visible result. The raw
text, structured result, timeout, exit code, and cause belong to the Tool call's
diagnostic observation. Before-Tool policies may change the effective input;
after-Tool policies, corrective context, guided error normalization, and context
projection may change the effective Tool Result. The terminal Tool Event's
`outcome` block is the recovery authority. A retained raw structured diagnostic
may explain that outcome, but must not replace or re-transform it.

Policy observation is one-way after the required checkpoint.
`PolicyObserver.Requested` is the commit gate and may fail closed before policy
code runs. `Started`, `Completed`, and `Errored`,
`PendingInputObserver.PendingInputsAdmitted`, and
`FinishPolicyContinuationObserver.FinishContinuationCommitted` have no flow
result. They must not mutate admission, policy selection, Tool Result content,
or Turn completion.

## Failure Matrix

Every test named below is an executable regression contract for its row. When
a checkpoint changes, update the behavior, recovery assertion, and referenced
test together without reopening unrelated ownership or ordering decisions.

| Checkpoint | Owner | Durable write | Live projection | State after failure | Recovery action | Regression test |
| --- | --- | --- | --- | --- | --- | --- |
| New main-input staging | `internal/runtime.PendingInputQueue` | `accepting` record in `pending_input.jsonl` | None; App still has no started result | Open or marshal failure leaves no record. Write, stat, or size-check failure returns before in-memory indexing, but the current queue writer does not synchronize and roll back its append, so disk may contain a partial or complete unindexed line | Retry only after queue reload validates the journal. An invalid tail must be repaired to the valid prefix before admission can continue; never infer acceptance from the transport error alone | `TestPendingInputQueue_TurnInputDoesNotExpireAndUsesOneAdmissionCheckpoint`; `TestPendingInputQueue_AppendFailureLeavesNoLiveRecordAndRequiresValidPrefixRepair`; `TestAdmitTurnMessage_IntentAppendFailureLeavesNoActiveTurn` |
| Main Turn admission | `internal/runtime.Engine.AdmitTurnMessage` through the Catalog-backed Bus | `turn.admitted` carrying the accepted message id, followed by `accepting -> admitted` | Status/Web update only after Event commit; App returns `started` only after promotion | Event failure clears the active reservation and attempts to drop a newly created intent. Without a committed admission Event for the same Framework-owned message id, any surviving `accepting` intent remains inert. A process stop after the Event commit but before queue promotion leaves an accepted record recoverable from the cross-journal facts. A pre-existing Pending record remains replayable | On startup, promote `accepting` only when a committed `turn.admitted` Event names the same message id; otherwise leave it inert. Turn ids alone are insufficient because transport allocators may restart their counters. Explicit `dropped` and `expired` records remain inert. Retry new transport input, or re-admit an existing durable Pending record | `TestAdmitTurnMessage_DurableAdmissionEventFailureDropsAcceptedInput`; `TestAdmitTurnMessage_AdmissionAndCompensationFailureCannotReplayInput`; `TestAdmitTurnMessage_FailedAdmissionKeepsPersistedInputReplayable`; `TestAdmitTurnMessage_IntentPromotionFailureCannotReplayInput`; `TestPendingInputQueue_ReconcileRecoveryFactsDoesNotMatchReusedTurnID`; `TestRecoverPendingInputRecordsUsesAdmissionEventsAndTranscriptFacts` |
| Recovered Turn input policy or projection | `internal/runtime.turnLifecycle` and `runtime/module` Turn-input policies | Existing `admitted` record, then the projected transcript message and `turn.started` | Policy and projection Events after their commit | Rejection or preparation failure ends the Turn; accepted input is appended once, with policy-blocked state when applicable, before `turn.errored` | Resume from transcript when append succeeded; otherwise retry recovery from the still-unprocessed durable record | `TestTurn_AcceptedInputIsReplayableBeforeTurnInputPolicy`; `TestTurn_ProjectionFailurePersistsAcceptedInputOnce`; `TestTurn_RecoveredAcceptedInputPolicyRejectsFailClosed` |
| Pending-input acceptance | `internal/runtime.Engine.EnqueuePendingMessageWithOptions` | `pending` record before the in-memory queue entry | `pending_input.queued` and status are best-effort projections after acceptance | Queue-full rejects without changing an already accepted persisted record. Queue append failure returns before live publication, but a write-phase failure has the same unresolved partial-or-unindexed disk boundary as main-input staging | Reload and identify the durable record before deciding whether the producer may retry; repair an invalid tail first. An already accepted persisted input is re-admitted rather than duplicated | `TestEngine_PendingInputBackpressure`; `TestPendingInputQueue_StagePersistedInputKeepsItReplayableUntilCommit`; `TestPendingInputQueue_AppendFailureLeavesNoLiveRecordAndRequiresValidPrefixRepair` |
| Pending-input drain | `internal/runtime.turnLifecycle` | `pending -> admitted`, projected transcript append, then `processed` | Drain/status Events and `PendingInputObserver` are non-authoritative | The uncommitted tail is prepended to the live queue; durable unprocessed records remain replayable. Terminal Turn failure attempts transcript repair and drains accepted input before closing | Retry in the same Turn when allowed. On restart, reconcile journal/Event/transcript facts, then App starts the oldest replayable record and Runtime drains the rest. App-facing synchronous turns and new external delivery serialize behind that startup recovery | `TestTurn_PersistedInputsAfterCurrentTriggerRestoreInOrder`; `TestTurn_CancellationPreservesPendingInputWithoutContinuing`; `TestTurn_ReplaysPersistedPendingInputAfterRestart`; `TestTurn_DoesNotReplayProcessedPendingInputAfterRestart`; `TestAppStartupReplaysDurablePendingInputWithoutNewUserTurn`; `TestAppRunWaitsForStartupPendingInputRecovery` |
| Tool-batch declaration | `internal/runtime.recordToolBatchLocked` | Complete ordered `tool.requested` set after `llm.responded` | Status/Web show declared calls only after commit | Any declaration failure aborts the batch before every Tool start; earlier declarations remain durable declared-only facts | Transcript repair emits `TOOL_NOT_STARTED` exactly once in Provider order | `TestTurn_DeclaresWholeToolBatchBeforeAnyToolStarts`; `TestTurn_DurableToolRequestFailurePreventsToolCall`; `TestToolExecutionRecoveryDistinguishesCrashBoundaries` |
| Tool start and transformed input | `internal/runtime.runToolCall` and `runtime/module` Tool policies | `tool.running`, then `tool.input_resolved` for each effective input change | Policy/Tool running projections follow committed facts | Start commit failure prevents all policy/handler work. Input checkpoint failure prevents later policy and handler work; recovery treats a started call without terminal outcome as uncertain | Repair emits `TOOL_OUTCOME_UNKNOWN`; verify external state before any manual retry | `TestTurn_DurableToolStartedFailurePreventsToolCall`; `TestTurn_TransformedToolInputIsDurableBeforeLaterPolicyExecution`; `TestTurn_DurableTransformedToolInputFailurePreventsHandlerExecution` |
| Tool terminal outcome | `internal/runtime.emitToolFinished` with `internal/toolevents` | `tool.completed` or `tool.errored` containing the exact projected Provider-visible Tool Result and message id | Terminal status is absorbing; live deltas are provisional and may be suppressed | A commit failure stops transcript append and the next Provider call. Handler side effects may already exist, so the durable state remains started-without-outcome | Repair reports `TOOL_OUTCOME_UNKNOWN`; never re-execute automatically | `TestTurn_DurableToolResultFailurePreventsNextProviderCall`; `TestTurn_DurableToolErrorFailurePersistsActualResult`; `TestTurn_DurableToolProjectionFailurePersistsProjectedResult`; `TestEndToEnd_DurableToolOutcomeResumesWithoutDuplicateExecution` |
| Tool-result transcript append | `internal/session.Session` after terminal Tool Events | Ordered projected Tool Result message in `conversation.jsonl` | No later Provider request occurs until append succeeds | Terminal outcomes are durable but transcript lacks the result batch | Transcript repair reconstructs the exact outcome blocks and appends them once | `TestToolExecutionRecoveryPreservesProviderOrderForMixedBatch`; `TestToolExecutionRecoveryDoesNotReclassifyNormalRecordedOutcomeAsRepair`; `TestEndToEnd_DurableToolOutcomeResumesWithoutDuplicateExecution` |
| Candidate Session selection and lock | `internal/app.AttachAndLockWorkspaceSession` and `internal/session` | Candidate Session and provisional active-history selection before lock acquisition; no App/Engine publication | Current App/status readers stay on the old Session, but another process reading history may observe the candidate | Lock failure closes the Session handle and clears the returned attachment, but the candidate record and active selection may remain. The caller has no candidate id, so its later cleanup and resident-selection restore path does not run | Treat persisted selection as unresolved and reconcile it against Session locks and the resident App before retry; never delete a candidate that another process may own | `TestAttachAndLockWorkspaceSession_NewPrimaryLockFailurePreservesProvisionalSelectionForReconciliation` |
| Candidate Session build, validation, or start | `internal/app.replaceSession` and `runtime/module` after successful attachment | Locked candidate Session and provisional active-history selection; no App/Engine publication | Current App/status readers stay on the old Session, but another process reading history may observe the candidate | Old App Session and Engine checkpoint remain authoritative; candidate Modules are closed. Candidate cleanup or resident-selection restore failure is joined with the rejection and leaves persisted history uncertain | The attachment caller closes and deletes the rejected candidate and reasserts the resident old App Session in active history. This may overwrite a concurrent selection; reconcile history before retry if restore failed | `TestSwitchToNewPrimarySessionBusyRestoresHistory`; `TestAppValidatesCompleteModuleCatalogBeforeSessionStart`; `TestAppValidatesCompleteModuleContextBeforeSessionStart`; `internal/runtime/module/lifecycle_test.go: TestSessionStartFailureRollsBackStartedModulesInReverseOrder` |
| Engine publication or Session-start policy | `internal/app.replaceSession` and `internal/runtime.ReplaceSessionRuntimeBundle` | Provisional active-history selection and candidate Engine bundle until App fields/status publish | Current App readers remain on old App state while the writer lock is held; another process may still observe candidate history | Busy publication rejects atomically. Later policy failure or cancellation starts rollback of the captured runtime and old Event/observability targets. Candidate Modules close only after the Engine restore succeeds; failed Engine rollback leaves them open because the Engine may still reference them. Rollback or subsequent resident-selection restore failure is joined with the rejection | Continue on the old Session only after Engine restore and the resident App Session history write both succeed; the caller cleans the rejected candidate, while a joined restore failure requires diagnosis and history reconciliation before reuse | `TestReplaceSessionRuntimeRejectsBusyRuntimeAtomically`; `TestSwitchToNewPrimarySessionStartPolicyRejectsReplacement`; `TestSwitchToNewPrimarySessionRollbackUsesCapturedRuntime`; `TestSwitchToNewPrimarySessionCancellationStopsSessionStartPolicy` |
| Committed Session replacement cleanup | `internal/app.replaceSession` | New App Session, Engine snapshot, Event journal target, and replayed status are authoritative | Readers see one coherent new Session after writer release | Failure closing old Modules, lock, or Session is diagnostic; rollback would be unsafe after publication | Keep the new Session active and surface the cleanup warning | `TestSwitchToNewPrimarySessionWaitsForLifecycleReaders`; `TestSwitchToNewPrimarySessionIsAtomicForConcurrentReaders`; `TestSwitchToNewPrimarySessionKeepsCommittedReplacementWhenOldModuleCleanupFails` |
| Finish-policy evaluation | `internal/runtime.turnLifecycle` and `runtime/module` | Durable assistant response already exists; policy context is queued only after all evaluations validate | Requested checkpoint may gate execution; later observer callbacks only report | Policy error, invalid context, or cancellation ends the Turn before any candidate is selected; no continuation is admitted | Resume from Session transcript and policy-owned durable state on later input | `TestTypedPoliciesPreserveModuleOrderAndEvaluateEveryFinishPolicy`; `TestFinishContinuationAndPolicyContextAreBounded`; `TestTurn_FinishPolicyOrdersBuiltInGatesAndStopHooks` |
| Candidate commit and continuation admission | Selected `runtime/module.FinishPolicy`, then `internal/runtime.PendingInputQueue` | Selected owner state first, then a durable continuation `pending` record | Continuation observer runs only after queue admission | A stale candidate changes nothing and evaluation falls through. Commit failure or queue failure ends the Turn; already committed owner state is not rolled back and no observer may claim admission | Retry from the durable owner state and transcript; only a real Pending record authorizes automatic continuation | `TestFinishCandidateCanBecomeStaleWithoutCommittingFlow`; `TestTurn_GoalCompletionGateContinuesThenCompletes`; `TestTurn_ContinuationQueueFailurePreservesCommittedPolicyStateWithoutObservation` |
| Final completion Event | `internal/runtime.turnLifecycle` | `turn.completed` after the active Turn closes; failure then attempts `turn.errored` | Status/Web/logs consume only a terminal Event that commits | Completion commit failure returns a terminal Turn error; the durable transcript remains, but no successful completion fact exists, and a wider journal failure may also prevent the error Event | Resume from transcript on a later admitted input; do not synthesize `turn.completed` from UI state | `TestTurn_CompletionCommitFailureReturnsErrorAndPreservesTranscript` |

## Test Index

Run focused lifecycle tests with:

```bash
go test ./internal/runtime ./internal/runtime/module ./internal/app ./internal/session
go test ./tests/e2e -run 'DurableToolOutcome|PendingInput'
```

The highest-signal suites are:

- `internal/runtime/loop_test.go`: admission, pending-input failure recovery,
  Tool checkpoint ordering, finish policy, cancellation, and terminal failures.
- `internal/runtime/policy_lifecycle_test.go`: compact golden order across Turn
  admission, input policy, Tool policy, Finish Policy, and completion.
- `internal/runtime/module/policy_test.go`: typed policy ordering, ownership,
  stale candidates, checkpoint failure, and observer non-authority.
- `internal/runtime/session_runtime_test.go`: coherent Engine bundle publication,
  busy rejection, provenance recovery, and exact checkpoint restoration.
- `internal/app/session_runtime_test.go`: candidate validation, rollback,
  reader atomicity, Session-start policy, and post-commit cleanup.
- `internal/runtime/pending_queue_test.go`: durable record states, replay order,
  stable identity, expiry, and processed deduplication.
- `internal/session/tool_execution_recovery_external_test.go` and
  `tests/e2e/tool_execution_recovery_test.go`: Tool crash-boundary repair and
  no duplicate execution after restart.
