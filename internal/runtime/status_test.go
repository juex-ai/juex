package runtime

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestSessionRuntimeStateIsWorking(t *testing.T) {
	tests := []struct {
		state SessionRuntimeState
		want  bool
	}{
		{state: SessionRuntimeIdle, want: false},
		{state: SessionRuntimeTurnActive, want: true},
		{state: SessionRuntimeDrainingPending, want: true},
		{state: SessionRuntimeFailed, want: false},
		{state: SessionRuntimeState("future"), want: false},
	}
	for _, test := range tests {
		t.Run(string(test.state), func(t *testing.T) {
			if got := test.state.IsWorking(); got != test.want {
				t.Fatalf("IsWorking() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestStatusErrorKindVocabulary(t *testing.T) {
	kinds := []StatusErrorKind{
		StatusErrorError,
		StatusErrorTimeout,
		StatusErrorCancelled,
		StatusErrorInterrupted,
		StatusErrorTerminated,
		StatusErrorPermission,
		StatusErrorAuth,
		StatusErrorPendingInputFull,
		StatusErrorCompaction,
		StatusErrorRuntimeRestart,
	}
	want := []string{
		"error",
		"timeout",
		"cancelled",
		"interrupted",
		"terminated",
		"permission",
		"auth",
		"pending_input_full",
		"compaction",
		"runtime_restart",
	}
	if len(kinds) != len(want) {
		t.Fatalf("kind count = %d, want %d", len(kinds), len(want))
	}
	for index := range kinds {
		if string(kinds[index]) != want[index] {
			t.Fatalf("kind[%d] = %q, want %q", index, kinds[index], want[index])
		}
	}

	cancellation := map[StatusErrorKind]bool{
		StatusErrorCancelled:      true,
		StatusErrorInterrupted:    true,
		StatusErrorTerminated:     true,
		StatusErrorRuntimeRestart: true,
	}
	for _, kind := range kinds {
		if got := kind.IsCancellation(); got != cancellation[kind] {
			t.Fatalf("%q IsCancellation() = %v, want %v", kind, got, cancellation[kind])
		}
	}
	for _, noncanonical := range []StatusErrorKind{"canceled", " CANCELLED ", "Cancelled", "future"} {
		if noncanonical.IsCancellation() {
			t.Fatalf("noncanonical kind %q classified as cancellation", noncanonical)
		}
	}
}

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
	assertTurnStatus(t, snapshot, TurnLifecycleAdmitted, "", false)

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
	snapshot = apply("14", "context.compact.completed", "turn-1", map[string]any{
		"context_usage": map[string]any{
			"model":          "compact-model",
			"context_window": 1000,
			"input_tokens":   40,
			"total_tokens":   40,
		},
	})
	assertTurnStatus(t, snapshot, TurnLifecycleActive, TurnPhaseToolBatch, false)
	if snapshot.ContextUsage == nil ||
		snapshot.ContextUsage.Model != "compact-model" ||
		snapshot.ContextUsage.TotalTokens != 40 {
		t.Fatalf("compacted context usage = %+v", snapshot.ContextUsage)
	}

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

	resumed := newStatusStoreFromSnapshot(atCursor)
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

func TestStatusStreamReturnsTransientStateAtSameDurableCursor(t *testing.T) {
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

	stream := store.OpenStream(StatusStreamOptions{After: cursor})
	defer stream.Close()
	snapshot, ok := stream.Next(context.Background())
	if !ok {
		t.Fatal("stream omitted current transient snapshot")
	}
	assertToolStatus(t, snapshot, "tool-1", ToolCallStreaming)
	if snapshot.Cursor != cursor {
		t.Fatalf("cursor = %q, want durable cursor %q", snapshot.Cursor, cursor)
	}
	if _, ok := stream.Next(context.Background()); ok {
		t.Fatal("stream returned more than the current transient snapshot")
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

	stream := store.OpenStream(StatusStreamOptions{After: admitted.ID})
	defer stream.Close()
	var snapshots []StatusSnapshot
	for {
		snapshot, ok := stream.Next(context.Background())
		if !ok {
			break
		}
		snapshots = append(snapshots, snapshot)
	}
	if len(snapshots) != 2 {
		t.Fatalf("replay snapshots = %d, want durable update plus current transient state", len(snapshots))
	}
	assertToolStatus(t, snapshots[1], "tool-1", ToolCallStreaming)
}

func TestStatusStoreConcurrentPublishDeliversFinalSnapshotLast(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "session-1", MaxPendingInputs: 1024})
	streams := make([]*StatusStream, 64)
	for i := range streams {
		streams[i] = store.OpenStream(StatusStreamOptions{Follow: true})
		if _, ok := streams[i].Next(context.Background()); !ok {
			t.Fatalf("stream %d omitted initial snapshot", i)
		}
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
	for i, stream := range streams {
		var last StatusSnapshot
		for {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
			snapshot, ok := stream.Next(ctx)
			cancel()
			if !ok {
				break
			}
			last = snapshot
		}
		if !reflect.DeepEqual(last, want) {
			t.Fatalf("stream %d last snapshot = %#v, want %#v", i, last, want)
		}
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
	resumed := newStatusStoreFromSnapshot(snapshot)
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
	assertTurnStatus(t, store.Snapshot(), TurnLifecycleAdmitted, "", false)
	store.Publish(statusEvent("4", "turn.completed", "compact-1", TurnCompletedPayload{}))

	snapshot := store.Snapshot()
	assertTurnStatus(t, snapshot, TurnLifecycleCompleted, "", false)
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

func TestStatusAutoCompactionRestoresAdmittedTurnUntilPhase(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "session-1"})
	store.Publish(statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}))
	store.Publish(statusEvent("2", "context.compact.started", "turn-1", ContextCompactStartedPayload{Auto: true}))
	store.Publish(statusEvent("3", "context.compact.completed", "turn-1", ContextCompactCompletedPayload{Auto: true}))

	snapshot := store.Snapshot()
	assertSessionStatus(t, snapshot, SessionRuntimeTurnActive, 0, true)
	assertTurnStatus(t, snapshot, TurnLifecycleAdmitted, "", false)

	store.Publish(statusEvent("4", "turn.started", "turn-1", TurnStartedPayload{Input: "next"}))
	assertTurnStatus(t, store.Snapshot(), TurnLifecycleAdmitted, "", false)
	store.Publish(statusEvent("5", TurnPhaseType, "turn-1", TurnPhasePayload{Phase: TurnPhaseProviderIteration}))
	assertTurnStatus(t, store.Snapshot(), TurnLifecycleActive, TurnPhaseProviderIteration, false)
}

func TestStatusSnapshotJSONResumePreservesInputsQueuedDuringDrain(t *testing.T) {
	eventsBeforeSnapshot := []events.Event{
		statusEvent("1", TurnAdmittedType, "turn-1", TurnAdmittedPayload{}),
		statusEvent("2", PendingInputDrainingType, "turn-1", PendingInputDrainingPayload{
			Count: 1, PendingCount: 0, MaxPendingInputs: 2,
		}),
	}
	eventsAfterSnapshot := []events.Event{
		statusEvent("3", "pending_input.queued", "turn-1", PendingInputQueuedPayload{
			PendingCount: 1, MaxPendingInputs: 2,
		}),
		statusEvent("4", "pending_input.drained", "turn-1", PendingInputDrainedPayload{
			Count: 1, PendingCount: 0, MaxPendingInputs: 2,
		}),
	}
	seed := StatusSeed{SessionID: "session-1", MaxPendingInputs: 2}
	direct := NewStatusStore(seed)
	for _, event := range append(eventsBeforeSnapshot, eventsAfterSnapshot...) {
		direct.Publish(event)
	}

	resumed := NewStatusStore(seed)
	for _, event := range eventsBeforeSnapshot {
		resumed.Publish(event)
	}
	encoded, err := json.Marshal(resumed.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var snapshot StatusSnapshot
	if err := json.Unmarshal(encoded, &snapshot); err != nil {
		t.Fatal(err)
	}
	resumed = newStatusStoreFromSnapshot(snapshot)
	for _, event := range eventsAfterSnapshot {
		resumed.Publish(event)
	}

	assertSessionStatus(t, resumed.Snapshot(), SessionRuntimeTurnActive, 1, true)
	if !reflect.DeepEqual(resumed.Snapshot(), direct.Snapshot()) {
		t.Fatalf("resumed = %#v\ndirect = %#v", resumed.Snapshot(), direct.Snapshot())
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
		statusEvent("4", toolevents.RequestedType, "turn-1", toolevents.RequestedPayload{
			Name: "exec_command", ToolUseID: "tool-1",
		}),
		statusEvent("5", toolevents.RunningType, "turn-1", toolevents.RunningPayload{
			Name: "exec_command", ToolUseID: "tool-1",
		}),
	})
	if tools := store.Snapshot().Tools; len(tools) != 1 || tools[0].State != ToolCallRunning {
		t.Fatalf("pre-recovery tools = %+v, want one running tool", tools)
	}
	store.RecoverAfterRestart()

	snapshot := store.Snapshot()
	if snapshot.Session.ID != "new" || snapshot.Session.Alias != "primary" {
		t.Fatalf("session = %+v", snapshot.Session)
	}
	if snapshot.Cursor != "5" {
		t.Fatalf("cursor = %q, want 5", snapshot.Cursor)
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
	if len(snapshot.Tools) != 0 {
		t.Fatalf("recovered tools = %+v, want none", snapshot.Tools)
	}
}

func TestStatusStreamSurvivesStoreReset(t *testing.T) {
	store := NewStatusStore(StatusSeed{SessionID: "old"})
	stream := store.OpenStream(StatusStreamOptions{Follow: true})
	defer stream.Close()
	initial, ok := stream.Next(context.Background())
	if !ok || initial.Session.ID != "old" {
		t.Fatalf("initial snapshot = %+v, %t", initial, ok)
	}
	store.Publish(statusEvent("5", TurnAdmittedType, "turn-old", TurnAdmittedPayload{}))

	store.Reset(StatusSeed{SessionID: "new"}, []events.Event{
		statusEvent("6", TurnAdmittedType, "turn-new", TurnAdmittedPayload{}),
	})
	replaced, ok := stream.Next(context.Background())
	if !ok ||
		replaced.Session.ID != "new" ||
		replaced.Cursor != "6" ||
		replaced.Turn == nil ||
		replaced.Turn.ID != "turn-new" {
		t.Fatalf("replacement snapshot = %+v, %t", replaced, ok)
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
