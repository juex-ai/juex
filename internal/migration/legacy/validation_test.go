package legacy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadThreadAcceptsAbsentEmptyInputQueue(t *testing.T) {
	dir, expected := legacyThreadFixture(t)
	if err := os.Remove(filepath.Join(dir, "inputs.json")); err != nil {
		t.Fatal(err)
	}
	got, err := ReadThread(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Inputs) != 0 || !reflect.DeepEqual(got.Context, expected) {
		t.Fatal("empty queue changed current context")
	}
	if !reflect.DeepEqual(got.AbsentFiles, []string{"inputs.json"}) {
		t.Fatal("missing queue must be captured as absence")
	}
}

func TestReadThreadAcceptsFailedWorkerAfterArchiveAndRestore(t *testing.T) {
	for _, restored := range []bool{false, true} {
		dir := legacyArchivedWorker(t)
		mutateLastFrame(t, dir, func(v map[string]any) {
			v["data"].(map[string]any)["facts"] = []any{map[string]any{"type": "turn.failed", "turn_id": "failed_turn", "error": "preserved terminal failure"}}
		})
		mutateMetadata(t, dir, func(v map[string]any) {
			v["counts"].(map[string]any)["turn_count"] = 1
			if restored {
				v["retention_state"], v["execution_state"] = "active", "idle"
				delete(v, "archived_at")
			}
		})
		got, err := ReadThread(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got.Commits[len(got.Commits)-1].Facts[0].Error != "preserved terminal failure" {
			t.Fatal("lost failure receipt")
		}
	}
}

func TestReadThreadRejectsIncompleteMetadataAndInput(t *testing.T) {
	for _, test := range []struct {
		name, file string
		change     func(map[string]any)
	}{
		{"missing input version", "inputs.json", func(v map[string]any) { delete(v, "v") }},
		{"missing usage", "thread.json", func(v map[string]any) { delete(v, "token_usage") }},
		{"missing usage cursor", "thread.json", func(v map[string]any) { delete(v, "usage_aggregated_through") }},
		{"invalid usage cursor", "thread.json", func(v map[string]any) { v["usage_aggregated_through"].(map[string]any)["generation_id"] = "g900000" }},
		{"usage mismatch", "thread.json", func(v map[string]any) {
			v["token_usage"].(map[string]any)["total"] = map[string]any{"input_tokens": 1, "output_tokens": 0}
		}},
		{"out of order timestamps", "thread.json", func(v map[string]any) { v["updated_at"] = "2020-01-01T00:00:00.000Z" }},
		{"archived Main", "thread.json", func(v map[string]any) {
			v["retention_state"], v["execution_state"], v["archived_at"] = "archived", "", v["created_at"]
		}},
		{"missing message identity", "inputs.json", func(v map[string]any) { delete(v["records"].([]any)[0].(map[string]any), "message_id") }},
		{"missing origin", "inputs.json", func(v map[string]any) { delete(v["records"].([]any)[0].(map[string]any), "origin") }},
		{"missing Turn", "inputs.json", func(v map[string]any) { delete(v["records"].([]any)[0].(map[string]any), "turn_id") }},
		{"invalid role", "inputs.json", func(v map[string]any) {
			v["records"].([]any)[0].(map[string]any)["message"].(map[string]any)["role"] = "assistant"
		}},
		{"settled error kind", "inputs.json", func(v map[string]any) { v["records"].([]any)[1].(map[string]any)["last_error_kind"] = "interrupted" }},
		{"duplicate input message", "inputs.json", func(v map[string]any) {
			first := v["records"].([]any)[0].(map[string]any)
			second := v["records"].([]any)[1].(map[string]any)
			second["message_id"], second["message"] = first["message_id"], first["message"]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, _ := legacyThreadFixture(t)
			path := filepath.Join(dir, test.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var v map[string]any
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatal(err)
			}
			test.change(v)
			writeFixtureJSON(t, path, v)
			before := fixtureHashes(t, dir)
			if _, err := ReadThread(dir); err == nil {
				t.Fatal("invalid source was accepted")
			}
			if !reflect.DeepEqual(before, fixtureHashes(t, dir)) {
				t.Fatal("reader changed invalid source")
			}
		})
	}
	t.Run("self parent", func(t *testing.T) {
		dir := legacyArchivedWorker(t)
		mutateMetadata(t, dir, func(v map[string]any) { v["parent_thread_id"] = v["thread_id"] })
		var metadata ThreadMetadata
		data, err := os.ReadFile(filepath.Join(dir, "thread.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := decode(data, &metadata); err != nil {
			t.Fatal(err)
		}
		if err := metadata.validate("abc123"); err == nil {
			t.Fatal("self parent accepted")
		}
	})
}
