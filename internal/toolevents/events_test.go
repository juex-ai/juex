package toolevents

import (
	"encoding/json"
	"testing"
)

func TestPayloadGoldenJSON(t *testing.T) {
	exitCode := 7
	call := ToolCallPayload{
		ToolUseID:      "call_1",
		Name:           "exec_command",
		Input:          map[string]any{"cmd": "printf ok"},
		TimeoutSeconds: 5,
	}

	tests := []struct {
		name    string
		payload any
		want    string
	}{
		{
			name:    "requested",
			payload: Requested(call),
			want:    `{"name":"exec_command","input":{"cmd":"printf ok"},"tool_use_id":"call_1","timeout_seconds":5}`,
		},
		{
			name: "output delta",
			payload: Delta(call, OutputDelta{
				SessionID: "42",
				ChunkID:   3,
				Stream:    "combined",
				Text:      "progress\r",
				Truncated: true,
			}),
			want: `{"name":"exec_command","tool_use_id":"call_1","session_id":"42","chunk_id":3,"stream":"combined","text":"progress\r","truncated":true}`,
		},
		{
			name:    "completed",
			payload: Completed(call, 5, 9, "ok output"),
			want:    `{"name":"exec_command","tool_use_id":"call_1","timeout_seconds":5,"len":9,"preview":"ok output"}`,
		},
		{
			name: "errored",
			payload: Errored(call, ErroredOptions{
				Error:          "exit status 7",
				TimeoutSeconds: 5,
				Len:            18,
				Preview:        "partial output",
				TimedOut:       true,
				ExitCode:       &exitCode,
			}),
			want: `{"name":"exec_command","tool_use_id":"call_1","error":"exit status 7","timeout_seconds":5,"len":18,"preview":"partial output","timed_out":true,"exit_code":7}`,
		},
		{
			name:    "errored omits absent optional fields",
			payload: Errored(call, ErroredOptions{Error: "denied"}),
			want:    `{"name":"exec_command","tool_use_id":"call_1","error":"denied","timeout_seconds":0}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.payload)
			if err != nil {
				t.Fatal(err)
			}
			if got := string(data); got != tt.want {
				t.Fatalf("json = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestDeltaAllowsToolAdapterOverrides(t *testing.T) {
	payload := Delta(
		ToolCallPayload{Name: "exec_command", ToolUseID: "call_1"},
		OutputDelta{Name: "write_stdin", ToolUseID: "call_2", SessionID: "9"},
	)
	if payload.Name != "write_stdin" || payload.ToolUseID != "call_2" || payload.SessionID != "9" {
		t.Fatalf("payload override = %+v", payload)
	}
}
