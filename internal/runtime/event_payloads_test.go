package runtime

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/juex-ai/juex/internal/llm"
)

func TestEventPayloadJSONShapePreservesConditionalFields(t *testing.T) {
	tests := []struct {
		name    string
		payload any
		want    map[string]any
	}{
		{
			name:    "turn started omits empty kind",
			payload: TurnStartedPayload{Input: "hello"},
			want:    map[string]any{"input": "hello"},
		},
		{
			name:    "pending queued keeps empty kind",
			payload: PendingInputQueuedPayload{Input: "queued", Kind: "", PendingCount: 1, MaxPendingInputs: 4},
			want: map[string]any{
				"input":              "queued",
				"kind":               "",
				"pending_count":      float64(1),
				"max_pending_inputs": float64(4),
			},
		},
		{
			name:    "tool error omits optional output fields",
			payload: ToolErroredPayload{Name: "read", ToolUseID: "tu1", Error: "missing", TimeoutSeconds: 0},
			want: map[string]any{
				"name":            "read",
				"tool_use_id":     "tu1",
				"error":           "missing",
				"timeout_seconds": float64(0),
			},
		},
		{
			name: "tool error includes output fields when present",
			payload: ToolErroredPayload{
				Name:           "shell",
				ToolUseID:      "tu2",
				Error:          "timeout",
				TimeoutSeconds: 1,
				Len:            6,
				Preview:        "stdout",
				TimedOut:       true,
			},
			want: map[string]any{
				"name":            "shell",
				"tool_use_id":     "tu2",
				"error":           "timeout",
				"timeout_seconds": float64(1),
				"len":             float64(6),
				"preview":         "stdout",
				"timed_out":       true,
			},
		},
		{
			name: "llm responded omits nil context usage",
			payload: LLMRespondedPayload{
				StopReason: llm.StopToolUse,
				Usage:      llm.Usage{InputTokens: 3, OutputTokens: 1},
				TokenUsage: llm.Usage{InputTokens: 8, OutputTokens: 2},
				Blocks: []llm.Block{{
					Type:      llm.BlockToolUse,
					ToolUseID: "tu3",
					ToolName:  "read",
					Input:     map[string]any{"path": "README.md"},
				}},
				Text:      "",
				Thinking:  "inspect",
				ToolCalls: []ToolCallPayload{{ToolUseID: "tu3", Name: "read", Input: map[string]any{"path": "README.md"}}},
				Model:     "mock:model",
			},
			want: map[string]any{
				"stop_reason": "tool_use",
				"usage": map[string]any{
					"input_tokens":  float64(3),
					"output_tokens": float64(1),
				},
				"token_usage": map[string]any{
					"input_tokens":  float64(8),
					"output_tokens": float64(2),
				},
				"blocks": []any{map[string]any{
					"type":        "tool_use",
					"tool_use_id": "tu3",
					"tool_name":   "read",
					"input":       map[string]any{"path": "README.md"},
				}},
				"text":     "",
				"thinking": "inspect",
				"tool_calls": []any{map[string]any{
					"tool_use_id":     "tu3",
					"name":            "read",
					"input":           map[string]any{"path": "README.md"},
					"timeout_seconds": float64(0),
				}},
				"model": "mock:model",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got map[string]any
			data, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("payload JSON = %#v, want %#v\njson: %s", got, tt.want, data)
			}
		})
	}
}
