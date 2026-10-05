package legacy

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadMemoryPreservesKnowledgeAndTerminalReceipts(t *testing.T) {
	dir := legacyMemoryFixture(t)
	before := fixtureHashes(t, dir)
	got, err := ReadMemory(dir, "fleet_fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].ID != "known_fact" || got.Entries[0].Body != "original knowledge\n" || got.Entries[0].Revision != 2 {
		t.Fatal("lost knowledge")
	}
	if got.State.Requests["receipt_applied"].Receipt.State != "applied" || got.State.Requests["receipt_failed"].Receipt.State != "failed" || len(got.State.Suppressed) != 1 || !got.State.Deleted["removed_fact"] {
		t.Fatal("lost receipts or deletion constraints")
	}
	if !reflect.DeepEqual(before, fixtureHashes(t, dir)) {
		t.Fatal("Memory reader wrote to source")
	}
	if len(got.AbsentFiles) != 1 || got.AbsentFiles[0] != "state/intent.json" {
		t.Fatal("missing intent must be captured as absence")
	}
}

func TestReadMemoryPreservesGenerationIndependentSuppression(t *testing.T) {
	dir := legacyMemoryFixture(t)
	mutateJSONFile(t, filepath.Join(dir, "state/state.json"), func(v map[string]any) {
		delete(v["suppressed"].([]any)[0].(map[string]any), "generation_id")
	})
	got, err := ReadMemory(dir, "fleet_fixture")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.State.Suppressed) != 1 || got.State.Suppressed[0].GenerationID != "" || got.State.Suppressed[0].From != 1 || got.State.Suppressed[0].Through != 8 {
		t.Fatal("cross-Generation no_store range changed")
	}
}

func TestReadMemoryRejectsInvalidSuppressionRange(t *testing.T) {
	for _, field := range []string{"fleet_id", "agent_id", "thread_id", "from", "through"} {
		t.Run(field, func(t *testing.T) {
			dir := legacyMemoryFixture(t)
			mutateJSONFile(t, filepath.Join(dir, "state/state.json"), func(v map[string]any) {
				delete(v["suppressed"].([]any)[0].(map[string]any), field)
			})
			if _, err := ReadMemory(dir, "fleet_fixture"); err == nil {
				t.Fatal("invalid no_store range accepted")
			}
		})
	}
}

func TestReadMemoryRejectsUnsettledOrInconsistentSource(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"interrupted commit", func(t *testing.T, dir string) {
			writeFixture(t, filepath.Join(dir, "state/intent.json"), []byte(`{"entries":[]}`))
		}},
		{"different Fleet", func(t *testing.T, dir string) {
			mutateJSONFile(t, filepath.Join(dir, "state/state.json"), func(v map[string]any) { v["fleet"] = "other_fleet" })
		}},
		{"active review", func(t *testing.T, dir string) {
			mutateJSONFile(t, filepath.Join(dir, "state/state.json"), func(v map[string]any) {
				v["requests"].(map[string]any)["receipt_failed"].(map[string]any)["receipt"].(map[string]any)["state"] = "running"
			})
		}},
		{"orphan request key", func(t *testing.T, dir string) {
			mutateJSONFile(t, filepath.Join(dir, "state/state.json"), func(v map[string]any) { v["keys"].(map[string]any)["key_applied"] = "missing_receipt" })
		}},
		{"changed entry identity", func(t *testing.T, dir string) {
			if err := os.Rename(filepath.Join(dir, "memory/known_fact.md"), filepath.Join(dir, "memory/wrong_id.md")); err != nil {
				t.Fatal(err)
			}
		}},
		{"deleted but retained", func(t *testing.T, dir string) {
			mutateJSONFile(t, filepath.Join(dir, "state/state.json"), func(v map[string]any) { v["deleted"].(map[string]any)["known_fact"] = true })
		}},
		{"unrecognized state", func(t *testing.T, dir string) {
			writeFixture(t, filepath.Join(dir, "state/new-format.json"), []byte(`{}`))
		}},
		{"source cursor reversal", func(t *testing.T, dir string) {
			mutateJSONFile(t, filepath.Join(dir, "state/state.json"), func(v map[string]any) {
				v["sources"].(map[string]any)["abc234/0"].(map[string]any)["processed_through"] = 100
			})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := legacyMemoryFixture(t)
			test.change(t, dir)
			before := fixtureHashes(t, dir)
			if _, err := ReadMemory(dir, "fleet_fixture"); err == nil {
				t.Fatal("unsafe Memory source accepted")
			}
			if !reflect.DeepEqual(before, fixtureHashes(t, dir)) {
				t.Fatal("reader recovered or rewrote source")
			}
		})
	}
}

func legacyMemoryFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"state", "memory"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	const at = "2026-09-25T01:02:03.000Z"
	source := map[string]any{"fleet_id": "fleet_fixture", "agent_id": "abc234", "thread_id": "0", "generation_id": "g000001", "from": 1, "through": 8}
	caller := map[string]any{"fleet_id": "fleet_fixture", "agent_id": "abc234", "thread_id": "0", "profile": "agent", "scope": map[string]any{}}
	work := func(id, state string, committed bool) map[string]any {
		return map[string]any{"receipt": map[string]any{"id": id, "state": state, "committed": committed, "index_ready": committed, "attempts": 1, "updated_at": at}, "proposal": map[string]any{"key": "key_" + state, "text": "private source proposal", "reason": "source evidence", "sources": []any{source}}, "caller": caller, "fingerprint": "preserved_fingerprint", "fence": 1, "automatic": false}
	}
	writeFixtureJSON(t, filepath.Join(dir, "state/state.json"), map[string]any{
		"fleet": "fleet_fixture", "strategy": "basic", "fence": 1, "clock": 2, "access": map[string]any{"known_fact": 1}, "uses": map[string]any{"known_fact": 2},
		"requests":   map[string]any{"receipt_applied": work("receipt_applied", "applied", true), "receipt_failed": work("receipt_failed", "failed", false)},
		"keys":       map[string]any{"key_applied": "receipt_applied", "key_failed": "receipt_failed"},
		"sources":    map[string]any{"abc234/0": map[string]any{"accepted_through": 8, "processed_through": 8, "epoch": "epoch_original", "enabled": true, "caller": caller, "evidence": []any{}, "ended_generations": []any{}, "idle_since": at, "first_pending": at, "pending": 0, "live_until": at}},
		"suppressed": []any{source}, "deleted": map[string]any{"removed_fact": true},
	})
	writeFixture(t, filepath.Join(dir, "memory/known_fact.md"), []byte("---\n"+`{"id":"known_fact","revision":2,"name":"Existing knowledge","summary":"Original summary","type":"fact","scope":{},"body":"","sources":[{"fleet_id":"fleet_fixture","agent_id":"abc234","thread_id":"0","generation_id":"g000001","from":1,"through":8}],"created_at":"2026-09-25T01:02:03.000Z","updated_at":"2026-09-25T01:02:03.000Z"}`+"\n---\noriginal knowledge\n"))
	writeFixture(t, filepath.Join(dir, "MEMORY.md"), []byte("original index\n"))
	return dir
}
