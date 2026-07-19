package runtime

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestStatusStoreProjectsLayeredTransitions(t *testing.T) {
	store := NewStatusStore(StatusSeed{
		SessionID:        "session-1",
		SessionAlias:     "primary",
		MaxPendingInputs: 2,
	})
	apply := func(id, eventType, turnID string, payload any) StatusSnapshot {
		t.Helper()
		store.Publish(statusEvent(id, eventType, turnID, payload))
		return store.Snapshot()
	}

	snapshot := apply("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{})
	assertSessionStatus(t, snapshot, SessionRuntimeTurnActive, 0, true)
	assertTurnStatus(t, snapshot, TurnLifecycleAdmitted, TurnPhaseAdmitted, false)

	snapshot = apply("2", TurnPhaseType, "turn-1", TurnPhasePayload{Phase: TurnPhaseProviderIteration})
	assertTurnStatus(t, snapshot, TurnLifecycleActive, TurnPhaseProviderIteration, false)

	snapshot = apply("3", "llm.requested", "turn-1", LLMRequestedPayload{Iter: 0})
	assertTurnStatus(t, snapshot, TurnLifecycleActive, TurnPhaseProviderIteration, true)

	snapshot = apply("4", "llm.responded", "turn-1", LLMRespondedPayload{
		TokenUsage: llm.Usage{InputTokens: 5, OutputTokens: 2},
		ContextUsage: &llm.ContextUsage{
			InputTokens: 5,
			TotalTokens: 5,
		},
	})
	assertTurnStatus(t, snapshot, TurnLifecycleActive, TurnPhaseProviderIteration, false)
	if snapshot.TokenUsage.InputTokens != 5 || snapshot.ContextUsage == nil || snapshot.ContextUsage.TotalTokens != 5 {
		t.Fatalf("usage snapshot = %+v / %+v", snapshot.TokenUsage, snapshot.ContextUsage)
	}

	snapshot = apply("5", TurnPhaseType, "turn-1", TurnPhasePayload{Phase: TurnPhaseToolBatch})
	assertTurnStatus(t, snapshot, TurnLifecycleActive, TurnPhaseToolBatch, false)

	tool := toolevents.ToolCallPayload{ToolUseID: "tool-1", Name: "exec_command"}
	snapshot = apply("6", toolevents.RequestedType, "turn-1", toolevents.Requested(tool))
	assertToolStatus(t, snapshot, "tool-1", ToolCallRequested)
	snapshot = apply("7", toolevents.RunningType, "turn-1", toolevents.Running(tool))
	assertToolStatus(t, snapshot, "tool-1", ToolCallRunning)
	snapshot = apply("8", toolevents.OutputDeltaType, "turn-1", toolevents.Delta(tool, toolevents.OutputDelta{Text: "hi"}))
	assertToolStatus(t, snapshot, "tool-1", ToolCallStreaming)
	snapshot = apply("9", toolevents.CompletedType, "turn-1", toolevents.Completed(tool, 60, 2, "ok", nil))
	assertToolStatus(t, snapshot, "tool-1", ToolCallCompleted)

	snapshot = apply("10", "pending_input.queued", "turn-1", PendingInputQueuedPayload{
		PendingCount: 1, MaxPendingInputs: 2,
	})
	assertSessionStatus(t, snapshot, SessionRuntimeTurnActive, 1, true)
	snapshot = apply("11", PendingInputDrainingType, "turn-1", PendingInputDrainingPayload{
		Count: 1, PendingCount: 0, MaxPendingInputs: 2,
	})
	assertSessionStatus(t, snapshot, SessionRuntimeDrainingPending, 0, true)
	snapshot = apply("12", "pending_input.drained", "turn-1", PendingInputDrainedPayload{
		Count: 1, PendingCount: 0, MaxPendingInputs: 2,
	})
	assertSessionStatus(t, snapshot, SessionRuntimeTurnActive, 0, true)

	snapshot = apply("13", "context.compact.started", "turn-1", ContextCompactStartedPayload{})
	assertTurnStatus(t, snapshot, TurnLifecycleActive, TurnPhaseCompacting, false)
	snapshot = apply("14", "context.compact.completed", "turn-1", ContextCompactCompletedPayload{})
	assertTurnStatus(t, snapshot, TurnLifecycleActive, TurnPhaseToolBatch, false)

	snapshot = apply("15", "turn.completed", "turn-1", TurnCompletedPayload{})
	assertSessionStatus(t, snapshot, SessionRuntimeIdle, 0, true)
	assertTurnStatus(t, snapshot, TurnLifecycleCompleted, TurnPhaseToolBatch, false)
	if len(snapshot.Tools) != 0 || snapshot.Cursor != "15" {
		t.Fatalf("terminal snapshot = %+v", snapshot)
	}
}

func TestStatusProjectionReachesEveryNamedState(t *testing.T) {
	tool := toolevents.ToolCallPayload{ToolUseID: "tool-1", Name: "exec_command"}
	admitted := statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{})
	requested := statusEvent("2", toolevents.RequestedType, "turn-1", toolevents.Requested(tool))
	running := statusEvent("3", toolevents.RunningType, "turn-1", toolevents.Running(tool))
	tests := []struct {
		name         string
		events       []events.Event
		toolState    ToolCallState
		turnState    TurnLifecycleState
		sessionState SessionRuntimeState
	}{
		{name: "tool requested", events: []events.Event{admitted, requested}, toolState: ToolCallRequested},
		{name: "tool running", events: []events.Event{admitted, requested, running}, toolState: ToolCallRunning},
		{
			name: "tool streaming",
			events: []events.Event{
				admitted, requested, running,
				statusEvent("4", toolevents.OutputDeltaType, "turn-1", toolevents.Delta(tool, toolevents.OutputDelta{Text: "partial"})),
			},
			toolState: ToolCallStreaming,
		},
		{
			name: "tool completed",
			events: []events.Event{
				admitted, requested, running,
				statusEvent("4", toolevents.CompletedType, "turn-1", toolevents.Completed(tool, 60, 2, "ok", nil)),
			},
			toolState: ToolCallCompleted,
		},
		{
			name: "tool errored",
			events: []events.Event{
				admitted, requested, running,
				statusEvent("4", toolevents.ErroredType, "turn-1", toolevents.Errored(tool, toolevents.ErroredOptions{Error: "failed"})),
			},
			toolState: ToolCallErrored,
		},
		{name: "turn admitted", events: []events.Event{admitted}, turnState: TurnLifecycleAdmitted},
		{
			name: "turn active",
			events: []events.Event{
				admitted,
				statusEvent("2", TurnPhaseType, "turn-1", TurnPhasePayload{Phase: TurnPhaseProviderIteration}),
			},
			turnState: TurnLifecycleActive,
		},
		{
			name: "turn completed",
			events: []events.Event{
				admitted,
				statusEvent("2", "turn.completed", "turn-1", TurnCompletedPayload{}),
			},
			turnState: TurnLifecycleCompleted,
		},
		{
			name: "turn errored",
			events: []events.Event{
				admitted,
				statusEvent("2", "turn.errored", "turn-1", TurnErroredPayload{Error: "failed"}),
			},
			turnState: TurnLifecycleErrored,
		},
		{
			name: "turn cancelled",
			events: []events.Event{
				admitted,
				statusEvent("2", "turn.errored", "turn-1", TurnErroredPayload{Error: "stopped", Interrupted: true}),
			},
			turnState: TurnLifecycleCancelled,
		},
		{name: "session idle", sessionState: SessionRuntimeIdle},
		{name: "session turn active", events: []events.Event{admitted}, sessionState: SessionRuntimeTurnActive},
		{
			name: "session draining pending",
			events: []events.Event{
				admitted,
				statusEvent("2", PendingInputDrainingType, "turn-1", PendingInputDrainingPayload{}),
			},
			sessionState: SessionRuntimeDrainingPending,
		},
		{
			name: "session failed",
			events: []events.Event{
				admitted,
				statusEvent("2", "turn.errored", "turn-1", TurnErroredPayload{Error: "failed"}),
			},
			sessionState: SessionRuntimeFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewStatusStore(StatusSeed{SessionID: "session-1"})
			for _, event := range test.events {
				store.Publish(event)
			}
			snapshot := store.Snapshot()
			if test.toolState != "" {
				assertToolStatus(t, snapshot, "tool-1", test.toolState)
			}
			if test.turnState != "" {
				if snapshot.Turn == nil || snapshot.Turn.State != test.turnState {
					t.Fatalf("turn = %+v, want state %q", snapshot.Turn, test.turnState)
				}
			}
			if test.sessionState != "" && snapshot.Session.State != test.sessionState {
				t.Fatalf("session = %+v, want state %q", snapshot.Session, test.sessionState)
			}
		})
	}
}

func TestStatusStoreDerivesQueueCapacityAndFailureRecovery(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "s", MaxPendingInputs: 1})
	store.Publish(statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}))
	store.Publish(statusEvent("2", "pending_input.queued", "turn-1", PendingInputQueuedPayload{
		PendingCount: 1, MaxPendingInputs: 1,
	}))
	full := store.Snapshot()
	assertSessionStatus(t, full, SessionRuntimeTurnActive, 1, false)

	store.Publish(statusEvent("3", "turn.errored", "turn-1", TurnErroredPayload{
		Error: "provider unavailable", ErrorKind: "error",
	}))
	failed := store.Snapshot()
	assertSessionStatus(t, failed, SessionRuntimeFailed, 1, false)
	if failed.LastError == nil || failed.LastError.Message != "provider unavailable" {
		t.Fatalf("last error = %+v", failed.LastError)
	}

	store.Publish(statusEvent("4", TurnAdmittedType, "turn-2", TurnAdmittedPayload{}))
	recovered := store.Snapshot()
	assertSessionStatus(t, recovered, SessionRuntimeTurnActive, 1, false)
	if recovered.LastError != nil {
		t.Fatalf("last error after recovery = %+v", recovered.LastError)
	}

	store.Publish(statusEvent("5", "turn.errored", "turn-2", TurnErroredPayload{
		Error: "cancelled", ErrorKind: "cancelled",
	}))
	cancelled := store.Snapshot()
	if cancelled.Turn == nil || cancelled.Turn.State != TurnLifecycleCancelled {
		t.Fatalf("cancelled turn = %+v", cancelled.Turn)
	}
}

func TestStatusSnapshotResumeIsDeterministic(t *testing.T) {
	all := []events.Event{
		statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}),
		statusEvent("2", TurnPhaseType, "turn-1", TurnPhasePayload{Phase: TurnPhaseProviderIteration}),
		statusEvent("3", "llm.requested", "turn-1", LLMRequestedPayload{Iter: 0}),
		statusEvent("4", "llm.responded", "turn-1", LLMRespondedPayload{}),
		statusEvent("5", "turn.completed", "turn-1", TurnCompletedPayload{}),
	}
	seed := StatusSeed{SessionID: "session-1", MaxPendingInputs: 4}
	first := NewStatusStore(seed)
	for _, event := range all[:3] {
		first.Publish(event)
	}
	atCursor := first.Snapshot()

	resumed := NewStatusStoreFromSnapshot(atCursor)
	for _, event := range all[3:] {
		resumed.Publish(event)
	}

	direct := NewStatusStore(seed)
	for _, event := range all {
		direct.Publish(event)
	}
	if !reflect.DeepEqual(resumed.Snapshot(), direct.Snapshot()) {
		t.Fatalf("resumed = %#v\ndirect = %#v", resumed.Snapshot(), direct.Snapshot())
	}
}

func TestStatusSubscriptionReturnsTransientStateAtSameDurableCursor(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "session-1"})
	store.Publish(statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}))
	cursor := store.Snapshot().Cursor

	transient := statusEvent("transient", toolevents.OutputDeltaType, "turn-1", toolevents.OutputDeltaPayload{
		ToolUseID: "tool-1",
		Name:      "exec_command",
		Text:      "partial",
	})
	transient.Transient = true
	store.Publish(transient)

	subscription := store.SubscribeFrom(cursor)
	defer subscription.Unsubscribe()
	if len(subscription.Snapshots) != 1 {
		t.Fatalf("replay snapshots = %d, want current transient snapshot", len(subscription.Snapshots))
	}
	assertToolStatus(t, subscription.Snapshots[0], "tool-1", ToolCallStreaming)
	if subscription.Snapshots[0].Cursor != cursor {
		t.Fatalf("cursor = %q, want durable cursor %q", subscription.Snapshots[0].Cursor, cursor)
	}
}

func TestStatusReplayKeepsTransientStateWithEqualTimestamp(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "session-1"})
	admitted := statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{})
	store.Publish(admitted)

	tool := toolevents.ToolCallPayload{ToolUseID: "tool-1", Name: "exec_command"}
	requested := statusEvent("2", toolevents.RequestedType, "turn-1", toolevents.Requested(tool))
	store.Publish(requested)
	transient := statusEvent("transient", toolevents.OutputDeltaType, "turn-1", toolevents.Delta(
		tool,
		toolevents.OutputDelta{Text: "partial"},
	))
	transient.Timestamp = requested.Timestamp
	transient.Transient = true
	store.Publish(transient)

	subscription := store.SubscribeFrom(admitted.ID)
	defer subscription.Unsubscribe()
	if len(subscription.Snapshots) != 2 {
		t.Fatalf("replay snapshots = %d, want durable update plus current transient state", len(subscription.Snapshots))
	}
	assertToolStatus(t, subscription.Snapshots[1], "tool-1", ToolCallStreaming)
}

func TestStatusStoreConcurrentPublishDeliversFinalSnapshotLast(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "session-1", MaxPendingInputs: 1024})
	subscriptions := make([]*StatusSubscription, 64)
	for i := range subscriptions {
		subscriptions[i] = store.SubscribeFrom("")
		defer subscriptions[i].Unsubscribe()
	}

	const publishCount = 256
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(publishCount)
	for i := 1; i <= publishCount; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			store.Publish(statusEvent(
				strconv.Itoa(i),
				"pending_input.queued",
				"",
				PendingInputQueuedPayload{PendingCount: i, MaxPendingInputs: 1024},
			))
		}(i)
	}
	close(start)
	wg.Wait()

	want := store.Snapshot()
	for i, subscription := range subscriptions {
		var last StatusSnapshot
		for {
			select {
			case last = <-subscription.Updates:
			default:
				if !reflect.DeepEqual(last, want) {
					t.Fatalf("subscription %d last snapshot = %#v, want %#v", i, last, want)
				}
				goto nextSubscription
			}
		}
	nextSubscription:
	}
}

func TestStatusSnapshotJSONResumeRestoresPreCompactionPhase(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "session-1"})
	store.Publish(statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}))
	store.Publish(statusEvent("2", TurnPhaseType, "turn-1", TurnPhasePayload{Phase: TurnPhaseToolBatch}))
	store.Publish(statusEvent("3", "context.compact.started", "turn-1", ContextCompactStartedPayload{}))

	encoded, err := json.Marshal(store.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot StatusSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	resumed := NewStatusStoreFromSnapshot(snapshot)
	resumed.Publish(statusEvent("4", "context.compact.completed", "turn-1", ContextCompactCompletedPayload{}))
	assertTurnStatus(t, resumed.Snapshot(), TurnLifecycleActive, TurnPhaseToolBatch, false)
}

func TestStatusStandaloneCompactionCompletesAdmittedTurn(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "session-1"})
	store.Publish(statusEvent("1", TurnAdmittedType, "compact-1", TurnAdmittedPayload{NonInterruptible: true}))
	if snapshot := store.Snapshot(); snapshot.Turn == nil || snapshot.Turn.CanInterrupt {
		t.Fatalf("admitted standalone compact turn = %+v, want non-interruptible", snapshot.Turn)
	}
	store.Publish(statusEvent("2", "context.compact.started", "compact-1", ContextCompactStartedPayload{}))
	store.Publish(statusEvent("3", "context.compact.completed", "compact-1", ContextCompactCompletedPayload{}))
	assertTurnStatus(t, store.Snapshot(), TurnLifecycleAdmitted, TurnPhaseAdmitted, false)
	store.Publish(statusEvent("4", "turn.completed", "compact-1", TurnCompletedPayload{}))

	snapshot := store.Snapshot()
	assertTurnStatus(t, snapshot, TurnLifecycleCompleted, TurnPhaseAdmitted, false)
	assertSessionStatus(t, snapshot, SessionRuntimeIdle, 0, true)
	if snapshot.Turn.ResumePhase != "" {
		t.Fatalf("standalone compact resume phase = %q", snapshot.Turn.ResumePhase)
	}
}

func TestStatusIgnoresLateToolOutputForTerminalOrSupersededTurn(t *testing.T) {
	tool := toolevents.ToolCallPayload{ToolUseID: "tool-1", Name: "exec_command"}
	withoutTurn := NewStatusStore(StatusSeed{SessionID: "session-1"})
	withoutTurn.Publish(statusEvent("1", toolevents.OutputDeltaType, "old-turn",
		toolevents.Delta(tool, toolevents.OutputDelta{Text: "late"})))
	emptySnapshot := withoutTurn.Snapshot()
	if emptySnapshot.Turn != nil ||
		emptySnapshot.Session.State != SessionRuntimeIdle ||
		len(emptySnapshot.Tools) != 0 {
		t.Fatalf("orphaned tool output changed status = %+v", emptySnapshot)
	}

	tests := []struct {
		name  string
		setup []events.Event
		want  TurnLifecycleState
	}{
		{
			name: "terminal turn",
			setup: []events.Event{
				statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}),
				statusEvent("2", toolevents.RequestedType, "turn-1", toolevents.Requested(tool)),
				statusEvent("3", "turn.completed", "turn-1", TurnCompletedPayload{}),
			},
			want: TurnLifecycleCompleted,
		},
		{
			name: "superseded turn",
			setup: []events.Event{
				statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}),
				statusEvent("2", toolevents.RequestedType, "turn-1", toolevents.Requested(tool)),
				statusEvent("3", TurnAdmittedType, "turn-2", TurnAdmittedPayload{}),
			},
			want: TurnLifecycleAdmitted,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewStatusStore(StatusSeed{SessionID: "session-1"})
			for _, event := range test.setup {
				store.Publish(event)
			}
			store.Publish(statusEvent("4", toolevents.OutputDeltaType, "turn-1",
				toolevents.Delta(tool, toolevents.OutputDelta{Text: "late"})))

			snapshot := store.Snapshot()
			if snapshot.Turn == nil || snapshot.Turn.State != test.want {
				t.Fatalf("turn = %+v, want state %q", snapshot.Turn, test.want)
			}
			if snapshot.Session.State == SessionRuntimeTurnActive && test.want == TurnLifecycleCompleted {
				t.Fatalf("terminal session reactivated = %+v", snapshot.Session)
			}
			if len(snapshot.Tools) != 0 {
				t.Fatalf("late tool output restored tools = %+v", snapshot.Tools)
			}
		})
	}
}

func TestStatusAutoCompactionRestoresAdmittedTurnUntilStart(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "session-1"})
	store.Publish(statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}))
	store.Publish(statusEvent("2", "context.compact.started", "turn-1", ContextCompactStartedPayload{Auto: true}))
	store.Publish(statusEvent("3", "context.compact.completed", "turn-1", ContextCompactCompletedPayload{Auto: true}))

	snapshot := store.Snapshot()
	assertSessionStatus(t, snapshot, SessionRuntimeTurnActive, 0, true)
	assertTurnStatus(t, snapshot, TurnLifecycleAdmitted, TurnPhaseAdmitted, false)

	store.Publish(statusEvent("4", "turn.started", "turn-1", TurnStartedPayload{Input: "next"}))
	assertTurnStatus(t, store.Snapshot(), TurnLifecycleActive, TurnPhaseProviderIteration, false)
}

func TestStatusDrainedEventDoesNotOverwriteInputQueuedDuringDrain(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "session-1", MaxPendingInputs: 2})
	store.Publish(statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}))
	store.Publish(statusEvent("2", PendingInputDrainingType, "turn-1", PendingInputDrainingPayload{
		Count: 1, PendingCount: 0, MaxPendingInputs: 2,
	}))
	store.Publish(statusEvent("3", "pending_input.queued", "turn-1", PendingInputQueuedPayload{
		PendingCount: 1, MaxPendingInputs: 2,
	}))
	store.Publish(statusEvent("4", "pending_input.drained", "turn-1", PendingInputDrainedPayload{
		Count: 1, PendingCount: 0, MaxPendingInputs: 2,
	}))

	assertSessionStatus(t, store.Snapshot(), SessionRuntimeTurnActive, 1, true)
}

func TestStatusStoreProjectsLegacyEventJournal(t *testing.T) {
	file, err := os.Open(filepath.Join("testdata", "legacy_status_events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()

	store := NewStatusStore(StatusSeed{SessionID: "legacy", MaxPendingInputs: 16})
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event events.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		store.Publish(event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	assertSessionStatus(t, snapshot, SessionRuntimeIdle, 0, true)
	if snapshot.Cursor != "legacy-8" || snapshot.Session.PendingCount != 0 {
		t.Fatalf("legacy snapshot = %+v, want cursor legacy-8 and empty pending queue", snapshot)
	}
}

func TestStatusStoreResetAndRecoverAfterRestart(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "old"})
	store.Reset(StatusSeed{SessionID: "new", SessionAlias: "primary", MaxPendingInputs: 3}, []events.Event{
		statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}),
		statusEvent("2", "llm.requested", "turn-1", LLMRequestedPayload{Iter: 0}),
		statusEvent("3", "pending_input.queued", "turn-1", PendingInputQueuedPayload{
			PendingCount: 3, MaxPendingInputs: 3,
		}),
	})
	store.RecoverAfterRestart()

	snapshot := store.Snapshot()
	if snapshot.Session.ID != "new" || snapshot.Session.Alias != "primary" {
		t.Fatalf("session = %+v", snapshot.Session)
	}
	if snapshot.Cursor != "3" {
		t.Fatalf("cursor = %q, want 3", snapshot.Cursor)
	}
	if snapshot.Turn == nil || snapshot.Turn.State != TurnLifecycleCancelled || snapshot.Turn.Streaming {
		t.Fatalf("turn = %+v", snapshot.Turn)
	}
	if snapshot.LastError == nil || snapshot.LastError.Kind != "runtime_restart" {
		t.Fatalf("last error = %+v", snapshot.LastError)
	}
	if snapshot.Session.PendingCount != 0 || !snapshot.Session.CanAcceptInput {
		t.Fatalf("recovered session queue = %+v, want empty and accepting input", snapshot.Session)
	}
}

func TestStatusStoreProjectsPromotedPendingInputCount(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "session-1", MaxPendingInputs: 2})
	store.Publish(statusEvent("1", TurnAdmittedType, "compact-1", TurnAdmittedPayload{}))
	store.Publish(statusEvent("2", "pending_input.queued", "compact-1", PendingInputQueuedPayload{
		PendingCount: 2, MaxPendingInputs: 2,
	}))
	store.Publish(statusEvent("3", PendingInputPromotedType, "turn-1", PendingInputPromotedPayload{
		PendingCount: 1, MaxPendingInputs: 2,
	}))
	store.Publish(statusEvent("4", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}))

	snapshot := store.Snapshot()
	if snapshot.Session.PendingCount != 1 || !snapshot.Session.CanAcceptInput {
		t.Fatalf("promoted queue status = %+v, want 1/2 and accepting input", snapshot.Session)
	}
}

func statusEvent(id, eventType, turnID string, payload any) events.Event {
	index := int(id[0] - '0')
	return events.Event{
		ID:        id,
		Type:      eventType,
		TurnID:    turnID,
		Timestamp: time.Date(2026, 7, 19, 0, 0, index, 0, time.UTC),
		Payload:   payload,
	}
}

func assertSessionStatus(t *testing.T, snapshot StatusSnapshot, state SessionRuntimeState, pending int, canAccept bool) {
	t.Helper()
	if snapshot.Session.State != state || snapshot.Session.PendingCount != pending || snapshot.Session.CanAcceptInput != canAccept {
		t.Fatalf("session = %+v, want state=%q pending=%d can_accept=%t", snapshot.Session, state, pending, canAccept)
	}
}

func assertTurnStatus(t *testing.T, snapshot StatusSnapshot, state TurnLifecycleState, phase TurnPhase, streaming bool) {
	t.Helper()
	if snapshot.Turn == nil {
		t.Fatal("turn = nil")
	}
	if snapshot.Turn.State != state || snapshot.Turn.Phase != phase || snapshot.Turn.Streaming != streaming {
		t.Fatalf("turn = %+v, want state=%q phase=%q streaming=%t", snapshot.Turn, state, phase, streaming)
	}
}

func assertToolStatus(t *testing.T, snapshot StatusSnapshot, toolUseID string, state ToolCallState) {
	t.Helper()
	for _, tool := range snapshot.Tools {
		if tool.ToolUseID == toolUseID {
			if tool.State != state {
				t.Fatalf("tool = %+v, want state %q", tool, state)
			}
			return
		}
	}
	t.Fatalf("tool %q not found in %+v", toolUseID, snapshot.Tools)
}
