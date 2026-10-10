package migration

import (
	"encoding/json"
	"testing"

	"github.com/juex-ai/juex/internal/migration/legacy"
)

func TestImportedInputOutcomeRequiresMatchingTerminalEvent(t *testing.T) {
	for _, fixture := range []struct{ state, kind, errorKind, want string }{
		{"settled", "turn.completed", "", "completed"},
		{"settled", "turn.cancelled", "", "cancelled"},
		{"settled", "turn.errored", "cancelled", "cancelled"},
		{"dead_lettered", "turn.errored", "error", "failed"},
		{"dead_lettered", "turn.errored", "timeout", "failed"},
		{"settled", "turn.errored", "runtime_restart", ""},
		{"settled", "turn.errored", "error", ""},
		{"dead_lettered", "turn.completed", "", ""},
	} {
		t.Run(fixture.state+"/"+fixture.kind+"/"+fixture.errorKind, func(t *testing.T) {
			input := legacy.Input{ID: "input", TurnID: "turn", State: fixture.state}
			if fixture.state == "dead_lettered" {
				input.LastErrorKind = fixture.errorKind
			}
			event, _ := json.Marshal(map[string]any{"type": fixture.kind, "turn_id": "turn", "payload": map[string]any{"input_ids": []string{"input"}, "error_kind": fixture.errorKind}})
			thread := legacy.Thread{Inputs: []legacy.Input{input}, Commits: []legacy.Commit{{Facts: []legacy.Fact{{Type: "event.recorded", Event: event}}}}}
			outcomes, err := inputOutcomes(thread)
			if fixture.want == "" {
				if err == nil {
					t.Fatal("contradictory input outcome accepted")
				}
				return
			}
			if err != nil || outcomes[input.ID] != fixture.want {
				t.Fatal("terminal meaning lost", outcomes, err)
			}
			thread.Commits = nil
			if _, err := inputOutcomes(thread); err == nil {
				t.Fatal("input state guessed without terminal event")
			}
			thread.Commits = []legacy.Commit{{Facts: []legacy.Fact{{Type: "event.recorded", Event: event}}}}
			thread.Inputs[0].TurnID = "different"
			if _, err := inputOutcomes(thread); err == nil {
				t.Fatal("terminal from another Turn accepted")
			}
		})
	}
}
