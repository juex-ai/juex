package statusapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/runtime"
)

func TestFromRuntimeProjectsPublicStatusWithoutRecoveryBookkeeping(t *testing.T) {
	now := time.Date(2026, 7, 20, 8, 30, 0, 0, time.UTC)
	source := runtime.StatusSnapshot{
		Cursor:    "cursor-1",
		UpdatedAt: now,
		Session: runtime.SessionRuntimeStatus{
			ID:               "session-one",
			Alias:            "primary",
			State:            runtime.SessionRuntimeTurnActive,
			PendingCount:     2,
			MaxPendingInputs: 4,
			CanAcceptInput:   true,
		},
		Turn: &runtime.TurnRuntimeStatus{
			ID:           "turn-one",
			State:        runtime.TurnLifecycleActive,
			Phase:        runtime.TurnPhaseCompacting,
			Streaming:    false,
			CanInterrupt: true,
			ResumeState:  runtime.TurnLifecycleActive,
			ResumePhase:  runtime.TurnPhaseToolBatch,
			StartedAt:    now,
			UpdatedAt:    now,
			Error: &runtime.StatusError{
				Message: "retrying",
				Kind:    runtime.StatusErrorCompaction,
			},
		},
		Tools: []runtime.ToolCallStatus{{
			ToolUseID: "tool-one",
			Name:      "exec_command",
			State:     runtime.ToolCallRunning,
			StartedAt: now,
			UpdatedAt: now,
		}},
		TokenUsage: llm.Usage{
			InputTokens:       11,
			OutputTokens:      3,
			CachedInputTokens: 5,
		},
		ContextUsage: &llm.ContextUsage{
			Model:         "test-model",
			ContextWindow: 100,
			InputTokens:   20,
			TotalTokens:   20,
			Breakdown: []llm.ContextUsagePart{{
				Key: "history", Label: "History", Tokens: 10,
			}},
		},
		LastError: &runtime.StatusError{
			Message: "last failure",
			Kind:    runtime.StatusErrorError,
		},
	}

	got := FromRuntime(source)
	if got.Session.ID != "session-one" || !got.Session.Working {
		t.Fatalf("session = %+v", got.Session)
	}
	if got.Turn == nil || got.Turn.ID != "turn-one" || got.Turn.Error == nil ||
		got.Turn.Error.Kind != StatusErrorCompaction {
		t.Fatalf("turn = %+v", got.Turn)
	}
	if got.TokenUsage.InputTokens != 11 || got.ContextUsage == nil ||
		got.ContextUsage.Breakdown[0].Key != "history" {
		t.Fatalf("usage = %+v / %+v", got.TokenUsage, got.ContextUsage)
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"resume_state", "resume_phase"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("public status leaked %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(string(body), `"working":true`) {
		t.Fatalf("public status omitted computed working: %s", body)
	}

	got.ContextUsage.Breakdown[0].Key = "mutated"
	if source.ContextUsage.Breakdown[0].Key != "history" {
		t.Fatal("DTO mutation changed runtime projection")
	}
}

func TestAgentActivityUsesOnlyAggregateContractFields(t *testing.T) {
	activity := AgentActivity{
		State:             ActivityWorking,
		PendingInputCount: 3,
		SelectedStatus: &Snapshot{
			Session: SessionStatus{
				ID:               "session-new",
				State:            SessionTurnActive,
				Working:          true,
				PendingCount:     2,
				MaxPendingInputs: 4,
				CanAcceptInput:   true,
			},
			Tools: []ToolCallStatus{},
		},
	}
	body, err := json.Marshal(activity)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`"pending_input_count":3`,
		`"selected_status"`,
		`"working":true`,
	} {
		if !strings.Contains(string(body), required) {
			t.Fatalf("activity omitted %q: %s", required, body)
		}
	}
	for _, forbidden := range []string{
		`"session_id"`,
		`"session_alias"`,
		`"pending_count":3`,
		`"status"`,
	} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("activity leaked compatibility field %q: %s", forbidden, body)
		}
	}

	var roundTrip AgentActivity
	if err := json.Unmarshal(body, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.State != ActivityWorking ||
		roundTrip.PendingInputCount != 3 ||
		roundTrip.SelectedStatus == nil ||
		!roundTrip.SelectedStatus.Session.Working {
		t.Fatalf("round trip = %+v", roundTrip)
	}
}
