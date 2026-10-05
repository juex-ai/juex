package legacy

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestReadThreadPreservesContextAndTerminalInputsWithoutWriting(t *testing.T) {
	dir, expected := legacyThreadFixture(t)
	before := fixtureHashes(t, dir)
	got, err := ReadThread(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata.ThreadID != "0" || got.Metadata.CurrentGeneration.ID != "g000002" || len(got.Commits) != 8 {
		t.Fatalf("lost identity or history: %+v", got.Metadata)
	}
	if !reflect.DeepEqual(got.Context, expected) {
		t.Fatalf("current context changed: got %+v, want %+v", got.Context, expected)
	}
	if len(got.Inputs) != 2 || got.Inputs[0].State != "dead_lettered" || got.Inputs[1].State != "settled" {
		t.Fatalf("terminal input state changed: %+v", got.Inputs)
	}
	if len(got.Files) != 4 || !reflect.DeepEqual(before, fixtureHashes(t, dir)) {
		t.Fatal("reader changed source files or omitted source fingerprints")
	}
	for _, file := range got.Files {
		if file.SHA256 != before[file.Path] {
			t.Fatal("incorrect source fingerprint", file)
		}
	}
}

func TestReadThreadRejectsUnsafeOrIncompleteSourceWithoutRepair(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"torn line", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "generations/g000002.jsonl")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, path, data[:len(data)-1])
		}},
		{"unfinished batch", func(t *testing.T, dir string) {
			mutateLastFrame(t, dir, func(value map[string]any) { value["count"] = float64(2) })
		}},
		{"sequence gap", func(t *testing.T, dir string) {
			mutateLastFrame(t, dir, func(value map[string]any) { value["data"].(map[string]any)["seq"] = float64(10) })
		}},
		{"unknown fact", func(t *testing.T, dir string) {
			mutateLastFrame(t, dir, func(value map[string]any) {
				value["data"].(map[string]any)["facts"].([]any)[0].(map[string]any)["type"] = "future.side_effect"
			})
		}},
		{"unknown field", func(t *testing.T, dir string) {
			mutateLastFrame(t, dir, func(value map[string]any) { value["data"].(map[string]any)["future_action"] = true })
		}},
		{"mixed batch counts", func(t *testing.T, dir string) {
			mutateFrame(t, dir, 1, func(value map[string]any) { value["index"], value["count"] = 0, 2 })
			mutateFrame(t, dir, 2, func(value map[string]any) { value["index"], value["count"] = 1, 3 })
		}},
		{"changed retained message", func(t *testing.T, dir string) {
			mutateFrame(t, dir, 0, func(value map[string]any) {
				seed := value["data"].(map[string]any)["facts"].([]any)[0].(map[string]any)["seed"].(map[string]any)
				seed["provider_messages"].([]any)[2].(map[string]any)["blocks"].([]any)[0].(map[string]any)["content"] = "different output"
			})
		}},
		{"invalid context scope", func(t *testing.T, dir string) {
			mutateFrame(t, dir, 0, func(value map[string]any) {
				seed := value["data"].(map[string]any)["facts"].([]any)[0].(map[string]any)["seed"].(map[string]any)
				seed["context_scope_id"] = "g999999"
			})
		}},
		{"missing tool result", func(t *testing.T, dir string) {
			mutateLastFrame(t, dir, func(value map[string]any) {
				fact := value["data"].(map[string]any)["facts"].([]any)[0].(map[string]any)
				fact["message"].(map[string]any)["blocks"] = []any{map[string]any{"type": "tool_use", "tool_use_id": "unsettled", "tool_name": "shell"}}
			})
		}},
		{"journal still working", func(t *testing.T, dir string) {
			mutateLastFrame(t, dir, func(value map[string]any) {
				value["data"].(map[string]any)["facts"] = []any{map[string]any{"type": "turn.started", "turn_id": "unfinished"}}
			})
		}},
		{"pending input", func(t *testing.T, dir string) {
			writeFixtureJSON(t, filepath.Join(dir, "inputs.json"), map[string]any{"v": 1, "records": []any{
				map[string]any{"id": "input_pending", "state": "pending", "created_at": "2026-09-24T01:02:03.000Z"},
			}})
		}},
		{"unregistered generation", func(t *testing.T, dir string) {
			writeFixture(t, filepath.Join(dir, "generations/g000003.jsonl"), []byte("uncommitted\n"))
		}},
		{"source cursor differs", func(t *testing.T, dir string) {
			mutateMetadata(t, dir, func(value map[string]any) { value["event_cursor"].(map[string]any)["offset"] = float64(1) })
		}},
		{"different identity", func(t *testing.T, dir string) {
			mutateMetadata(t, dir, func(value map[string]any) { value["thread_id"] = "other1" })
		}},
		{"source symlink", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "inputs.json")
			target := filepath.Join(t.TempDir(), "private.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			writeFixture(t, target, data)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir, _ := legacyThreadFixture(t)
			test.change(t, dir)
			before := fixtureHashes(t, dir)
			if _, err := ReadThread(dir); err == nil {
				t.Fatal("unsafe or incomplete source was accepted")
			}
			if !reflect.DeepEqual(before, fixtureHashes(t, dir)) {
				t.Fatal("failed reader repaired or modified the source")
			}
		})
	}
}

func TestReadThreadAcceptsCompleteBatchesAndRenewedContext(t *testing.T) {
	dir, expected := legacyThreadFixture(t)
	mutateFrame(t, dir, 1, func(v map[string]any) { v["index"], v["count"] = 0, 2 })
	mutateFrame(t, dir, 2, func(v map[string]any) { v["index"], v["count"] = 1, 2 })
	got, err := ReadThread(dir)
	if err != nil || !reflect.DeepEqual(got.Context, expected) {
		t.Fatalf("complete batch: %v", err)
	}
	mutateFrame(t, dir, 0, func(v map[string]any) {
		v["data"].(map[string]any)["facts"] = []any{map[string]any{"type": "context.renewed", "from_generation_id": "g000001", "to_generation_id": "g000002",
			"seed": map[string]any{"v": 1, "context_scope_id": "g000002"}}}
	})
	got, err = ReadThread(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Context, expected[3:]) || len(got.Commits) != 8 {
		t.Fatal("renewal must clear current context while retaining original history")
	}
}

func TestReadThreadPreservesArchivedWorkerRelationship(t *testing.T) {
	worker := legacyArchivedWorker(t)
	got, err := ReadThread(worker)
	if err != nil {
		t.Fatal(err)
	}
	if got.Metadata.ThreadID != "abc123" || got.Metadata.ParentThreadID != "0" || got.Metadata.RetentionState != "archived" {
		t.Fatal("lost retained worker identity")
	}
}

func legacyArchivedWorker(t *testing.T) string {
	t.Helper()
	dir, _ := legacyThreadFixture(t)
	worker := filepath.Join(filepath.Dir(dir), "abc123")
	if err := os.Rename(dir, worker); err != nil {
		t.Fatal(err)
	}
	mutateMetadata(t, worker, func(v map[string]any) {
		v["thread_id"], v["parent_thread_id"], v["alias"] = "abc123", "0", "retained worker"
		v["retention_state"], v["execution_state"], v["archived_at"] = "archived", "", "2026-09-25T01:02:03.000Z"
		v["updated_at"] = v["archived_at"]
	})
	path := filepath.Join(worker, "generations/g000001.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(data, []byte{'\n'})
	var first map[string]any
	if err := json.Unmarshal(lines[0], &first); err != nil {
		t.Fatal(err)
	}
	fact := first["data"].(map[string]any)["facts"].([]any)[0].(map[string]any)
	fact["thread_id"], fact["parent_thread_id"], fact["alias"] = "abc123", "0", "original worker name"
	lines[0], err = json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, bytes.Join(lines, []byte{'\n'}))
	return worker
}

func legacyThreadFixture(t *testing.T) (string, []llm.Message) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "0")
	if err := os.MkdirAll(filepath.Join(dir, "generations"), 0o700); err != nil {
		t.Fatal(err)
	}
	message := func(id string, role llm.Role, text string) llm.Message {
		v := llm.TextMessage(role, text)
		v.ID = id
		return v
	}
	old := message("msg_old", llm.RoleUser, "old original question")
	call := llm.Message{ID: "msg_call", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "original_call", ToolName: "read", Input: map[string]any{"path": "original.txt"}}}}
	result := llm.Message{ID: "msg_result", Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "original_call", ToolName: "read", Content: "original tool output"}}}
	answer := message("msg_answer", llm.RoleAssistant, "old answer outside the retained context")
	summary := message("msg_summary", llm.RoleUser, "preserved compact context")
	summary.Kind = llm.MessageKindCompact
	summary.Compaction = &llm.CompactionMetadata{RetainedMessageIDs: []string{call.ID, result.ID}}
	after := message("msg_after", llm.RoleUser, "new question")
	final := message("msg_final", llm.RoleAssistant, "new answer")
	const at = "2026-09-24T01:02:03.000Z"
	frame := func(seq int, fact map[string]any) []byte {
		v, err := json.Marshal(map[string]any{"v": 1, "index": 0, "count": 1, "data": map[string]any{"v": 1, "seq": seq, "at": at, "facts": []any{fact}}})
		if err != nil {
			t.Fatal(err)
		}
		return append(v, '\n')
	}
	first := frame(1, map[string]any{"type": "thread.created", "thread_id": "0", "alias": "main", "generation_id": "g000001"})
	for i, msg := range []llm.Message{old, call, result, answer} {
		first = append(first, frame(i+2, map[string]any{"type": "message.appended", "generation_id": "g000001", "message": msg})...)
	}
	second := frame(6, map[string]any{"type": "context.compacted", "from_generation_id": "g000001", "to_generation_id": "g000002", "summary": summary,
		"seed": map[string]any{"v": 1, "context_scope_id": "g000001", "provider_messages": []llm.Message{summary, call, result}}})
	for i, msg := range []llm.Message{after, final} {
		second = append(second, frame(i+7, map[string]any{"type": "message.appended", "generation_id": "g000002", "message": msg})...)
	}
	writeFixture(t, filepath.Join(dir, "generations/g000001.jsonl"), first)
	writeFixture(t, filepath.Join(dir, "generations/g000002.jsonl"), second)
	metadata := map[string]any{"v": 1, "thread_id": "0", "alias": "main", "retention_state": "active", "execution_state": "idle", "revision": 8,
		"created_at": at, "updated_at": at, "last_activity_at": at,
		"current_generation":       map[string]any{"generation_id": "g000002", "ordinal": 2, "boundary_seq": 6},
		"generations":              []any{map[string]any{"generation_id": "g000001", "ordinal": 1, "boundary_seq": 1}, map[string]any{"generation_id": "g000002", "ordinal": 2, "boundary_seq": 6}},
		"counts":                   map[string]any{"generation_count": 2, "turn_count": 0, "pending_input_count": 0},
		"token_usage":              map[string]any{"total": llm.Usage{}, "by_model": map[string]any{}},
		"usage_aggregated_through": map[string]any{"generation_id": "g000002", "seq": 8, "offset": len(second)},
		"event_cursor":             map[string]any{"generation_id": "g000002", "seq": 8, "offset": len(second)}}
	writeFixtureJSON(t, filepath.Join(dir, "thread.json"), metadata)
	writeFixtureJSON(t, filepath.Join(dir, "inputs.json"), map[string]any{"v": 1, "records": []any{
		map[string]any{"id": "input_dead", "state": "dead_lettered", "message": old, "message_id": old.ID, "origin": "turn", "turn_id": "turn_dead", "created_at": at},
		map[string]any{"id": "input_settled", "state": "settled", "message": after, "message_id": after.ID, "origin": "queued", "turn_id": "turn_settled", "created_at": at},
	}})
	return dir, []llm.Message{summary, call, result, after, final}
}

func mutateMetadata(t *testing.T, dir string, change func(map[string]any)) {
	t.Helper()
	mutateJSONFile(t, filepath.Join(dir, "thread.json"), change)
}

func mutateJSONFile(t *testing.T, path string, change func(map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	change(value)
	writeFixtureJSON(t, path, value)
}

func mutateLastFrame(t *testing.T, dir string, change func(map[string]any)) {
	t.Helper()
	mutateFrame(t, dir, -1, change)
}

func mutateFrame(t *testing.T, dir string, index int, change func(map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, "generations/g000002.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
	if index < 0 {
		index = len(lines) - 1
	}
	var value map[string]any
	if err := json.Unmarshal(lines[index], &value); err != nil {
		t.Fatal(err)
	}
	change(value)
	lines[index], err = json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	data = append(bytes.Join(lines, []byte{'\n'}), '\n')
	writeFixture(t, path, data)
	mutateMetadata(t, dir, func(value map[string]any) {
		value["event_cursor"].(map[string]any)["offset"] = len(data)
		value["usage_aggregated_through"].(map[string]any)["offset"] = len(data)
	})
}

func writeFixtureJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, path, data)
}

func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func fixtureHashes(t *testing.T, dir string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		result[relative] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
