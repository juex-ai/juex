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
)

func TestReadAgentPreservesPrivateStateAndVerifiedReferences(t *testing.T) {
	dir := legacyAgentFixture(t)
	before := fixtureHashes(t, dir)
	got, err := ReadAgent(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Definition.ID != "abc234" || len(got.Threads) != 1 || got.Threads[0].Metadata.ThreadID != "0" {
		t.Fatal("lost Agent/Thread identity")
	}
	if len(got.References) != 2 {
		t.Fatalf("references: %+v", got.References)
	}
	files := map[string]SourceFile{}
	for _, f := range got.Files {
		files[f.Path] = f
		if f.SHA256 != before[f.Path] {
			t.Fatal("wrong fingerprint", f.Path)
		}
	}
	for _, path := range []string{"juex.yaml", "modules/memory-client/participation.json", "extensions/calendar/calendar.json", "threads/0/spool/tool-results/original.txt", "media/read-media/image.png"} {
		if _, ok := files[path]; !ok {
			t.Fatal("lost persistent file", path)
		}
	}
	for _, path := range []string{"runtime.json", "logs/process.log", "tmp/cache", "threads/0/logs/process.log", "modules/memory/.lock"} {
		if _, ok := files[path]; ok {
			t.Fatal("included operational file", path)
		}
	}
	if !reflect.DeepEqual(before, fixtureHashes(t, dir)) {
		t.Fatal("source changed")
	}
}

func TestReadAgentRejectsBrokenReferencesAndOwnership(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testing.T, string)
	}{
		{"missing spool", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, "threads/0/spool/tool-results/original.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"changed media", func(t *testing.T, dir string) {
			writeFixture(t, filepath.Join(dir, "media/read-media/image.png"), []byte("different bytes"))
		}},
		{"traversal reference", func(t *testing.T, dir string) {
			mutateAgentMessages(t, dir, func(m map[string]any) {
				for _, v := range m["blocks"].([]any) {
					b := v.(map[string]any)
					if a, ok := b["artifact"].(map[string]any); ok {
						a["stored_path"] = "../../private.txt"
					}
				}
			})
		}},
		{"symlink private state", func(t *testing.T, dir string) {
			path := filepath.Join(dir, "extensions/calendar/calendar.json")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "calendar.json")
			writeFixture(t, outside, []byte(`{}`))
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
		}},
		{"unknown persistent root", func(t *testing.T, dir string) {
			writeFixture(t, filepath.Join(dir, "new-business-state.json"), []byte(`{}`))
		}},
		{"unsettled trash", func(t *testing.T, dir string) {
			path := filepath.Join(dir, ".trash/threads")
			writeFixture(t, filepath.Join(path, "pending-delete"), []byte("retained"))
		}},
		{"wrong Agent identity", func(t *testing.T, dir string) {
			data, err := os.ReadFile(filepath.Join(dir, "agent.json"))
			if err != nil {
				t.Fatal(err)
			}
			var v map[string]any
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatal(err)
			}
			v["id"] = "def456"
			writeFixtureJSON(t, filepath.Join(dir, "agent.json"), v)
		}},
		{"archived Main", func(t *testing.T, dir string) {
			archive := filepath.Join(dir, "archive/threads")
			if err := os.MkdirAll(archive, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(filepath.Join(dir, "threads/0"), filepath.Join(archive, "0")); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := legacyAgentFixture(t)
			test.change(t, dir)
			before := fixtureHashes(t, dir)
			if _, err := ReadAgent(dir); err == nil {
				t.Fatal("invalid source was accepted")
			}
			if !reflect.DeepEqual(before, fixtureHashes(t, dir)) {
				t.Fatal("source changed after rejection")
			}
		})
	}
}

func TestReadAgentAcceptsDownsampledMediaOriginalSize(t *testing.T) {
	dir := legacyAgentFixture(t)
	mutateAgentMessages(t, dir, func(m map[string]any) {
		for _, v := range m["blocks"].([]any) {
			if ref, ok := v.(map[string]any)["media"].(map[string]any); ok {
				ref["original_bytes"] = 99999
			}
		}
	})
	if _, err := ReadAgent(dir); err != nil {
		t.Fatal("original source size is not the stored downsampled image size", err)
	}
}

func TestReadAgentRejectsAmbiguousThreadNamesAcrossArchives(t *testing.T) {
	for _, alias := range []string{"MAIN", "0", "ABC123"} {
		t.Run(alias, func(t *testing.T) {
			dir := legacyAgentFixture(t)
			worker := legacyArchivedWorker(t)
			mutateMetadata(t, worker, func(v map[string]any) { v["alias"] = alias })
			if _, err := ReadThread(worker); err != nil {
				t.Fatal("individual Thread must remain valid", err)
			}
			archive := filepath.Join(dir, "archive/threads")
			if err := os.MkdirAll(archive, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(worker, filepath.Join(archive, "abc123")); err != nil {
				t.Fatal(err)
			}
			if _, err := ReadAgent(dir); err == nil {
				t.Fatal("ambiguous Agent Thread names accepted")
			}
		})
	}
}

func TestReadAgentPreservesOpaqueExtensionNames(t *testing.T) {
	dir := legacyAgentFixture(t)
	for _, name := range []string{"extensions/calendar/logs/entries.json", "extensions/calendar/.lock"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0700); err != nil {
			t.Fatal(err)
		}
		writeFixture(t, filepath.Join(dir, name), []byte("extension-owned content"))
	}
	got, err := ReadAgent(dir)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, file := range got.Files {
		if string(file.Data) == "extension-owned content" {
			count++
		}
	}
	if count != 2 {
		t.Fatal("opaque extension state was treated as operational logs or locks")
	}
}

func legacyAgentFixture(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "abc234")
	for _, name := range []string{"threads", "extensions/calendar", "modules/memory-client", "modules/memory", "media/read-media", "logs", "tmp", ".trash/threads"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	thread, _ := legacyThreadFixture(t)
	if err := os.Rename(thread, filepath.Join(dir, "threads/0")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"threads/0/spool/tool-results", "threads/0/logs"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	writeFixtureJSON(t, filepath.Join(dir, "agent.json"), map[string]any{"id": "abc234", "name": "Source Agent", "workspace": t.TempDir(), "created_at": "2026-09-23T00:00:00Z", "enabled": true, "autostart": true})
	writeFixture(t, filepath.Join(dir, "juex.yaml"), []byte("agent:\n  name: Source Agent\n"))
	writeFixture(t, filepath.Join(dir, "extensions/calendar/calendar.json"), []byte(`{"version":1,"entries":[]}`))
	writeFixture(t, filepath.Join(dir, "modules/memory-client/participation.json"), []byte(`{"enabled":true}`))
	for _, name := range []string{"runtime.json", "logs/process.log", "tmp/cache", "threads/0/logs/process.log", "modules/memory/.lock"} {
		writeFixture(t, filepath.Join(dir, name), []byte("operational"))
	}
	spool, media := []byte("complete original tool result"), []byte("synthetic media bytes")
	writeFixture(t, filepath.Join(dir, "threads/0/spool/tool-results/original.txt"), spool)
	writeFixture(t, filepath.Join(dir, "media/read-media/image.png"), media)
	spoolSum, mediaSum := sha256.Sum256(spool), sha256.Sum256(media)
	mutateAgentMessages(t, dir, func(m map[string]any) {
		if m["id"] == "msg_result" {
			m["blocks"].([]any)[0].(map[string]any)["artifact"] = map[string]any{"source_kind": "tool_result", "stored_path": "tool-results/original.txt", "sha256": hex.EncodeToString(spoolSum[:]), "original_bytes": len(spool)}
		}
		if m["id"] == "msg_after" {
			blocks := m["blocks"].([]any)
			m["blocks"] = append(blocks, map[string]any{"type": "image", "media": map[string]any{"artifact_path": "read-media/image.png", "sha256": hex.EncodeToString(mediaSum[:]), "original_bytes": len(media), "media_type": "image/png"}})
		}
	})
	return dir
}

func mutateAgentMessages(t *testing.T, dir string, change func(map[string]any)) {
	t.Helper()
	var visit func(any)
	visit = func(v any) {
		switch v := v.(type) {
		case map[string]any:
			if _, ok := v["role"]; ok {
				if _, ok := v["blocks"]; ok {
					change(v)
				}
			}
			for _, x := range v {
				visit(x)
			}
		case []any:
			for _, x := range v {
				visit(x)
			}
		}
	}
	thread := filepath.Join(dir, "threads/0")
	for _, gen := range []string{"g000001", "g000002"} {
		path := filepath.Join(thread, "generations", gen+".jsonl")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		lines := bytes.Split(bytes.TrimSuffix(data, []byte{'\n'}), []byte{'\n'})
		for i, line := range lines {
			var v any
			if err := json.Unmarshal(line, &v); err != nil {
				t.Fatal(err)
			}
			visit(v)
			lines[i], err = json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
		}
		data = append(bytes.Join(lines, []byte{'\n'}), '\n')
		writeFixture(t, path, data)
		if gen == "g000002" {
			mutateMetadata(t, thread, func(v map[string]any) {
				v["event_cursor"].(map[string]any)["offset"] = len(data)
				v["usage_aggregated_through"].(map[string]any)["offset"] = len(data)
			})
		}
	}
}
