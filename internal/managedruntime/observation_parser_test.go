package managedruntime

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func TestObservationParserPreservesByteIdentityAcrossPages(t *testing.T) {
	line := []byte("{\"type\":\"notification\",\"method\":\"notifications/claude/channel\",\"params\":{\"text\":\"你好\"}}\n")
	data := append(append([]byte{}, line...), line...)
	source := ObservationSource{EnvironmentID: "environment", OperationID: "operation", Kind: "mcp_connect"}
	var facts []Observation
	for offset := 0; offset < len(data); {
		next := min(offset+7, len(data))
		op := ToolOperation{State: "running", Snapshot: execprotocol.Snapshot{Output: data[offset:next], NextCursor: int64(next), OutputBytes: int64(len(data))}}
		batch, err := parseObservation(source, op)
		if err != nil {
			t.Fatal(err)
		}
		facts = append(facts, batch.Facts...)
		source.Cursor, source.Pending, source.Discarding = batch.Cursor, batch.Pending, batch.Discarding
		offset = next
	}
	if len(facts) != 2 || facts[0].ID == facts[1].ID || facts[0].Offset != int64(len(line)) || facts[1].Offset != int64(len(data)) || !json.Valid(facts[0].Data) {
		t.Fatal(facts)
	}
	again, err := parseObservation(ObservationSource{EnvironmentID: source.EnvironmentID, OperationID: source.OperationID, Kind: source.Kind}, ToolOperation{State: "completed", Snapshot: execprotocol.Snapshot{Output: data, NextCursor: int64(len(data)), OutputBytes: int64(len(data))}})
	if err != nil || len(again.Facts) != 3 || again.Facts[0].ID != facts[0].ID || !again.Closed || again.Facts[2].Kind != "operation.terminal" {
		t.Fatal(again, err)
	}
}

func TestObservationParserMakesMalformedAndOversizedRecordsVisible(t *testing.T) {
	data := append(bytes.Repeat([]byte("x"), execprotocol.MaxMCPEventBytes+1), '\n')
	data = append(data, []byte("bad-json\n{\"type\":\"connected\"}\n{\"type\":\"stderr\",\"text\":\"log\"}\n{\"type\":\"notification\",\"method\":\"notifications/test\"}\npartial")...)
	source := ObservationSource{EnvironmentID: "environment", OperationID: "operation", Kind: "mcp_connect"}
	var facts []Observation
	for offset := 0; offset < len(data); {
		next := min(offset+64<<10, len(data))
		state := "running"
		if next == len(data) {
			state = "failed"
		}
		batch, err := parseObservation(source, ToolOperation{State: state, Snapshot: execprotocol.Snapshot{Output: data[offset:next], NextCursor: int64(next), OutputBytes: int64(len(data))}})
		if err != nil {
			t.Fatal(err)
		}
		facts = append(facts, batch.Facts...)
		if len(batch.Pending) > execprotocol.MaxMCPEventBytes {
			t.Fatal("unbounded partial line")
		}
		source.Cursor, source.Pending, source.Discarding = batch.Cursor, batch.Pending, batch.Discarding
		offset = next
	}
	if len(facts) != 5 || facts[0].Kind != "mcp.invalid_notification" || facts[1].Kind != "mcp.invalid_notification" || facts[2].Kind != "mcp.notification" || facts[3].Kind != "mcp.invalid_notification" || facts[4].Kind != "operation.terminal" {
		t.Fatal(facts)
	}
}

func TestObservationParserBoundsBatchesWithoutLosingEvents(t *testing.T) {
	data := bytes.Repeat([]byte("x\n"), 1200)
	source := ObservationSource{EnvironmentID: "environment", OperationID: "operation", Kind: "mcp_connect"}
	facts := map[string]bool{}
	for {
		batch, err := parseObservation(source, ToolOperation{State: "completed", Snapshot: execprotocol.Snapshot{Output: data[source.Cursor:], NextCursor: int64(len(data)), OutputBytes: int64(len(data))}})
		if err != nil {
			t.Fatal(err)
		}
		if len(batch.Facts) > 513 {
			t.Fatal("unbounded batch", len(batch.Facts))
		}
		for _, fact := range batch.Facts {
			if facts[fact.ID] {
				t.Fatal("duplicate record", fact)
			}
			facts[fact.ID] = true
		}
		if batch.Closed {
			break
		}
		if batch.Cursor <= source.Cursor || !batch.More {
			t.Fatal("no progress", batch)
		}
		source.Cursor = batch.Cursor
	}
	if len(facts) != 1201 {
		t.Fatal("lost records", len(facts))
	}
	batch, err := parseObservation(source, ToolOperation{State: "completed", Snapshot: execprotocol.Snapshot{NextCursor: source.Cursor, OutputBytes: source.Cursor, OutputExpired: true}})
	if err != nil || !batch.Closed || batch.More || len(batch.Facts) != 1 || batch.Facts[0].Kind != "operation.output_expired" {
		t.Fatal("expired source must stop visibly", batch, err)
	}
}
