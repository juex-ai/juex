package managedruntime

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func observationEnvelope(text string) []byte {
	data, _ := json.Marshal(map[string]any{"type": "observation", "stream": "stdout", "text": text, "time": time.Now().UTC()})
	return append(data, '\n')
}
func TestCommandObserverPersistsPartialRecordsAndBatches(t *testing.T) {
	source := ObservationSource{EnvironmentID: "device", OperationID: "command", Kind: "observe_command", Options: execprotocol.ObservableOptions{Parser: execprotocol.ObservableParser{Type: "jsonl", ContentField: "message"}, Batch: execprotocol.ObservableBatch{IntervalSeconds: 10, MaxChars: 3}}}
	data := observationEnvelope(`{"message":"你好世界"}`)
	var facts []Observation
	for cursor := 0; cursor < len(data); {
		next := min(cursor+3, len(data))
		batch, err := parseObservation(source, ToolOperation{State: "running", Snapshot: execprotocol.Snapshot{Output: data[cursor:next], NextCursor: int64(next), OutputBytes: int64(len(data))}})
		if err != nil {
			t.Fatal(err)
		}
		facts = append(facts, batch.Facts...)
		source.Cursor, source.Pending, source.Command = batch.Cursor, batch.Pending, batch.Command
		// Model a database round trip between every chunk.
		encoded, _ := json.Marshal(source)
		_ = json.Unmarshal(encoded, &source)
		cursor = next
	}
	if len(facts) != 0 || len(source.Command.Units) != 1 || source.Command.Units[0].Content != "你好世界" {
		t.Fatal(facts, source)
	}
	source.Command.Deadline = time.Now().Add(-time.Second)
	batch, err := parseObservation(source, ToolOperation{State: "running", Snapshot: execprotocol.Snapshot{NextCursor: source.Cursor, OutputBytes: source.Cursor}})
	if err != nil || len(batch.Facts) != 1 || len(batch.Command.Units) != 0 || !strings.Contains(string(batch.Facts[0].Data), `"content":"你好世"`) || !strings.Contains(string(batch.Facts[0].Data), `"full_content":"你好世界"`) {
		t.Fatal(batch, err)
	}
	source.Command = batch.Command
	batch, err = parseObservation(source, ToolOperation{State: "completed", Snapshot: execprotocol.Snapshot{NextCursor: source.Cursor, OutputBytes: source.Cursor}})
	if err != nil || !batch.Closed || len(batch.Facts) != 1 || batch.Facts[0].Kind != "operation.terminal" {
		t.Fatal(batch, err)
	}
}
func TestCommandObserverFiltersHaveStableDistinctBatchAndAttachmentIDs(t *testing.T) {
	attachments := make([]map[string]string, 16)
	for i := range attachments {
		attachments[i] = map[string]string{"path": "same.txt", "name": strings.Repeat("n", i+1)}
	}
	raw, _ := json.Marshal(map[string]any{"text": "match", "files": attachments})
	data := observationEnvelope(string(raw))
	source := ObservationSource{EnvironmentID: "device", OperationID: "command", Kind: "observe_command", Options: execprotocol.ObservableOptions{Parser: execprotocol.ObservableParser{Type: "jsonl", ContentField: "text", AttachmentsField: "files"}, Filters: []execprotocol.ObservableFilter{{Contains: "match", Kind: "first"}, {Contains: "match", Kind: "second"}}}}
	op := ToolOperation{State: "completed", Snapshot: execprotocol.Snapshot{Output: data, NextCursor: int64(len(data)), OutputBytes: int64(len(data))}}
	batch, err := parseObservation(source, op)
	if err != nil || len(batch.Facts) != 3 || batch.Facts[0].ID == batch.Facts[1].ID {
		t.Fatal(batch, err)
	}
	replay, err := parseObservation(source, op)
	if err != nil || replay.Facts[0].ID != batch.Facts[0].ID || replay.Facts[1].ID != batch.Facts[1].ID {
		t.Fatal("unstable replay", replay, err)
	}
	var value struct{ Attachments []ObservationAttachment }
	if json.Unmarshal(batch.Facts[0].Data, &value) != nil || len(value.Attachments) != 16 || value.Attachments[0].Ordinal == value.Attachments[1].Ordinal {
		t.Fatal(value)
	}
}
func TestCommandObserverInvalidOversizedAndExitEvents(t *testing.T) {
	source := ObservationSource{EnvironmentID: "device", OperationID: "command", Kind: "observe_command", Options: execprotocol.ObservableOptions{Parser: execprotocol.ObservableParser{Type: "jsonl"}, OnExit: "nonzero"}}
	data := append(bytes.Repeat([]byte("x"), 513<<10), '\n')
	data = append(data, observationEnvelope("invalid json")...)
	data = append(data, []byte("partial")...)
	batch, err := parseObservation(source, ToolOperation{State: "failed", Snapshot: execprotocol.Snapshot{Output: data, NextCursor: int64(len(data)), OutputBytes: int64(len(data))}})
	if err != nil || !batch.Closed || len(batch.Facts) != 5 {
		t.Fatal(batch, err)
	}
	if batch.Facts[3].Kind != "command.exit" || batch.Facts[4].Kind != "operation.terminal" {
		t.Fatal(batch)
	}
}
