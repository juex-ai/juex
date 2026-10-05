package migration

import (
	"encoding/json"
	"errors"

	"github.com/juex-ai/juex/internal/migration/legacy"
)

// The source queue retains both completion and cancellation as "settled".
// Its recorded terminal events are the authority for the narrower new states.
func inputOutcomes(thread legacy.Thread) (map[string]string, error) {
	type terminal struct{ turn, state, errorKind string }
	last := map[string]terminal{}
	wanted := map[string]bool{}
	for _, input := range thread.Inputs {
		wanted[input.ID] = true
	}
	for _, commit := range thread.Commits {
		for _, fact := range commit.Facts {
			if fact.Type != "event.recorded" {
				continue
			}
			var event struct {
				Type    string          `json:"type"`
				TurnID  string          `json:"turn_id"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(fact.Event, &event); err != nil {
				return nil, err
			}
			if event.Type != "turn.completed" && event.Type != "turn.cancelled" && event.Type != "turn.errored" {
				continue
			}
			var payload struct {
				InputIDs  []string `json:"input_ids"`
				ErrorKind string   `json:"error_kind"`
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				return nil, err
			}
			state := "failed"
			switch {
			case event.Type == "turn.completed":
				state = "completed"
			case event.Type == "turn.cancelled" || payload.ErrorKind == "cancelled":
				state = "cancelled"
			case payload.ErrorKind == "runtime_restart" || payload.ErrorKind == "interrupted" || payload.ErrorKind == "terminated":
				state = "retryable"
			}
			for _, id := range payload.InputIDs {
				if wanted[id] {
					last[id] = terminal{event.TurnID, state, payload.ErrorKind}
				}
			}
		}
	}
	out := map[string]string{}
	for _, input := range thread.Inputs {
		event, exists := last[input.ID]
		if !exists || event.turn != input.TurnID || event.turn == "" {
			return nil, errors.New("source input has no matching terminal Turn event")
		}
		switch input.State {
		case "settled":
			if event.state != "completed" && event.state != "cancelled" {
				return nil, errors.New("settled source input contradicts its terminal event")
			}
		case "dead_lettered":
			if event.state != "failed" || event.errorKind != input.LastErrorKind {
				return nil, errors.New("failed source input contradicts its terminal event")
			}
		default:
			return nil, errors.New("source input is not terminal")
		}
		out[input.ID] = event.state
	}
	return out, nil
}
