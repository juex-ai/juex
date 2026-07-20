package app

import (
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/runtime"
)

func TestTurnAdmissionStatusProjectionReleasesBeforeTerminalStatus(t *testing.T) {
	tests := []struct {
		name      string
		event     events.Event
		wantState runtime.TurnLifecycleState
	}{
		{
			name:      "completed",
			event:     events.Event{ID: "completed", Type: "turn.completed", TurnID: "turn-1"},
			wantState: runtime.TurnLifecycleCompleted,
		},
		{
			name: "errored",
			event: events.Event{
				ID:      "errored",
				Type:    "turn.errored",
				TurnID:  "turn-1",
				Payload: runtime.TurnErroredPayload{Error: "boom"},
			},
			wantState: runtime.TurnLifecycleErrored,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			status := runtime.NewStatusStore(runtime.StatusSeed{
				SessionID:        "session-1",
				MaxPendingInputs: runtime.DefaultMaxPendingInput,
			})
			admission := turnAdmission{
				phase:  turnAdmissionRunning,
				turnID: "turn-1",
			}
			terminalPublishedBeforeRelease := false
			projection := turnAdmissionStatusProjection{
				status: status,
				completeTurn: func(turnID string) {
					if turn := status.Snapshot().Turn; turn != nil &&
						(turn.State == runtime.TurnLifecycleCompleted ||
							turn.State == runtime.TurnLifecycleErrored) {
						terminalPublishedBeforeRelease = true
					}
					turnAdmissionQueue{state: &admission}.complete(turnID)
				},
			}

			projection.Publish(events.Event{
				ID:     "admitted",
				Type:   runtime.TurnAdmittedType,
				TurnID: "turn-1",
			})
			if phase, turnID := (turnAdmissionQueue{state: &admission}).snapshot(); phase != turnAdmissionRunning || turnID != "turn-1" {
				t.Fatalf("non-terminal admission = (%q, %q), want running turn-1", phase, turnID)
			}

			projection.Publish(test.event)

			if terminalPublishedBeforeRelease {
				t.Fatal("terminal status was published before admission release")
			}
			if phase, turnID := (turnAdmissionQueue{state: &admission}).snapshot(); phase != turnAdmissionIdle || turnID != "" {
				t.Fatalf("admission after terminal event = (%q, %q), want idle", phase, turnID)
			}
			snapshot := status.Snapshot()
			if snapshot.Turn == nil || snapshot.Turn.State != test.wantState {
				t.Fatalf("terminal status = %+v, want state %q", snapshot.Turn, test.wantState)
			}
		})
	}
}

func TestIsTerminalTurnEvent(t *testing.T) {
	for _, test := range []struct {
		eventType string
		want      bool
	}{
		{eventType: "turn.completed", want: true},
		{eventType: "turn.errored", want: true},
		{eventType: runtime.TurnPhaseType, want: false},
		{eventType: "context.compact.completed", want: false},
	} {
		if got := isTerminalTurnEvent(test.eventType); got != test.want {
			t.Errorf("isTerminalTurnEvent(%q) = %v, want %v", test.eventType, got, test.want)
		}
	}
}
