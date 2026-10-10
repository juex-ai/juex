package native

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type writeMutationReader struct {
	reader io.Reader
	mutate func()
}

func (r *writeMutationReader) Read(p []byte) (int, error) {
	if r.mutate != nil {
		mutate := r.mutate
		r.mutate = nil
		mutate()
	}
	return r.reader.Read(p)
}

func TestChunkedWriteRechecksTargetAfterStaging(t *testing.T) {
	for _, replace := range []string{"directory", "file"} {
		t.Run(replace, func(t *testing.T) {
			dir := t.TempDir()
			parent := filepath.Join(dir, "d")
			path := filepath.Join(parent, "file")
			if err := os.Mkdir(parent, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("original"), 0644); err != nil {
				t.Fatal(err)
			}
			target, err := prepareWriteTarget(dir, "d/file", "overwrite")
			if err != nil {
				t.Fatal(err)
			}
			reader := &writeMutationReader{reader: strings.NewReader("buffer"), mutate: func() {
				if replace == "directory" {
					if err := os.Rename(parent, parent+"-old"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(parent, 0755); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Rename(path, path+"-old"); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(path, []byte("external"), 0644); err != nil {
					t.Fatal(err)
				}
			}}
			if err := publishWrite(context.Background(), *target, execprotocol.FileManifest{Size: 6, SHA256: digest([]byte("buffer"))}, reader); err == nil {
				t.Fatal("stale target published")
			}
			data, _ := os.ReadFile(path)
			if string(data) != "external" {
				t.Fatal("external replacement changed", string(data))
			}
			original := path + "-old"
			if replace == "directory" {
				original = filepath.Join(parent+"-old", "file")
			}
			data, _ = os.ReadFile(original)
			if string(data) != "original" {
				t.Fatal("detached target changed", string(data))
			}
		})
	}
}

func writeTestEngine(t *testing.T, config Config) *Engine {
	t.Helper()
	e, err := Open(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}
func writeTestConfig(t *testing.T) Config {
	return Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: "environment", WorkingDirectory: t.TempDir(), Grants: map[string][]execprotocol.Capability{"agent": {execprotocol.Files}}, Retention: time.Second}
}
func writeRequest(id, kind string, args any) execprotocol.Request {
	data, _ := json.Marshal(args)
	return execprotocol.Request{Version: execprotocol.Version, ID: id, AgentID: "agent", Kind: kind, Arguments: data, WriteContext: &execprotocol.WriteContext{ThreadID: "thread", ResetID: "reset"}}
}
func runWriteTest(t *testing.T, e *Engine, request execprotocol.Request, want execprotocol.State) execprotocol.Snapshot {
	t.Helper()
	snapshot, err := e.Submit(request)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !snapshot.State.Terminal() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		snapshot, err = e.Snapshot(request.AgentID, request.ID, 0, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
	}
	if snapshot.State != want {
		t.Fatalf("%s: got %s (%s), want %s", request.ID, snapshot.State, snapshot.Error, want)
	}
	return snapshot
}
func TestChunkedWriteDurableOrderValidationAndReceipt(t *testing.T) {
	config := writeTestConfig(t)
	e := writeTestEngine(t, config)
	path := filepath.Join(config.WorkingDirectory, "file")
	if err := os.WriteFile(path, []byte("original"), 0750); err != nil {
		t.Fatal(err)
	}
	begin := runWriteTest(t, e, writeRequest("begin", "write_begin", map[string]any{"path": "file"}), execprotocol.Completed)
	if begin.Write == nil || begin.Write.WriteID != "begin" {
		t.Fatal(begin)
	}
	for _, index := range []int{2, 0} {
		runWriteTest(t, e, writeRequest("chunk"+string(rune('0'+index)), "write_chunk", map[string]any{"write_id": "begin", "index": index, "content": []string{"A", "中", "C"}[index]}), execprotocol.Completed)
	}
	runWriteTest(t, e, writeRequest("gap", "write_commit", map[string]any{"write_id": "begin"}), execprotocol.Failed)
	data, _ := os.ReadFile(path)
	if string(data) != "original" {
		t.Fatal("validation changed target")
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e = writeTestEngine(t, config)
	chunk := runWriteTest(t, e, writeRequest("chunk1", "write_chunk", map[string]any{"write_id": "begin", "index": 1, "content": "中"}), execprotocol.Completed)
	duplicate := runWriteTest(t, e, writeRequest("duplicate", "write_chunk", map[string]any{"write_id": "begin", "index": 1, "content": "中"}), execprotocol.Completed)
	if !chunk.Write.At.Equal(duplicate.Write.At) {
		t.Fatal("duplicate refreshed TTL")
	}
	runWriteTest(t, e, writeRequest("conflict", "write_chunk", map[string]any{"write_id": "begin", "index": 1, "content": "other"}), execprotocol.Failed)
	runWriteTest(t, e, writeRequest("count", "write_commit", map[string]any{"write_id": "begin", "expected_chunks": 4}), execprotocol.Failed)
	runWriteTest(t, e, writeRequest("hash", "write_commit", map[string]any{"write_id": "begin", "sha256": strings.Repeat("0", 64)}), execprotocol.Failed)
	request := writeRequest("commit", "write_commit", map[string]any{"write_id": "begin", "expected_chunks": 3, "sha256": strings.ToUpper(digest([]byte("A中C")))})
	committed := runWriteTest(t, e, request, execprotocol.Completed)
	if committed.Write == nil || committed.Write.Chunks != 3 || committed.Write.Bytes != 5 {
		t.Fatal(committed)
	}
	data, _ = os.ReadFile(path)
	info, _ := os.Stat(path)
	if string(data) != "A中C" || info.Mode().Perm() != 0750 {
		t.Fatal(string(data), info)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e = writeTestEngine(t, config)
	if err := os.WriteFile(path, []byte("later"), 0750); err != nil {
		t.Fatal(err)
	}
	replayed := runWriteTest(t, e, request, execprotocol.Completed)
	if !reflect.DeepEqual(replayed.Write, committed.Write) {
		t.Fatal("receipt changed")
	}
	data, _ = os.ReadFile(path)
	if string(data) != "later" {
		t.Fatal("replayed commit")
	}
	runWriteTest(t, e, writeRequest("new-commit", "write_commit", map[string]any{"write_id": "begin"}), execprotocol.Failed)
}

func TestChunkedWriteValidationAbortAndCreateRace(t *testing.T) {
	config := writeTestConfig(t)
	e := writeTestEngine(t, config)
	runWriteTest(t, e, writeRequest("begin", "write_begin", map[string]any{"path": "new", "mode": "create"}), execprotocol.Completed)
	runWriteTest(t, e, writeRequest("alias", "write_begin", map[string]any{"path": filepath.Join(config.WorkingDirectory, "new")}), execprotocol.Failed)
	cases := []map[string]any{
		{"index": -1, "content": "a"}, {"index": 0.5, "content": "a"}, {"content": "a"}, {"index": 0},
		{"index": 0, "content": strings.Repeat("a", 2001)}, {"index": 0, "content": strings.Repeat("中", 1334)},
		{"index": 4096, "content": "a"}, {"index": 0, "content": "a", "sha256": "bad"},
	}
	for i, args := range cases {
		args["write_id"] = "begin"
		runWriteTest(t, e, writeRequest("bad"+string(rune('a'+i)), "write_chunk", args), execprotocol.Failed)
	}
	runWriteTest(t, e, writeRequest("zero", "write_commit", map[string]any{"write_id": "begin"}), execprotocol.Failed)
	runWriteTest(t, e, writeRequest("empty", "write_chunk", map[string]any{"write_id": "begin", "index": 0, "content": ""}), execprotocol.Completed)
	path := filepath.Join(config.WorkingDirectory, "new")
	if err := os.WriteFile(path, []byte("external"), 0644); err != nil {
		t.Fatal(err)
	}
	runWriteTest(t, e, writeRequest("race", "write_commit", map[string]any{"write_id": "begin"}), execprotocol.Failed)
	data, _ := os.ReadFile(path)
	if string(data) != "external" {
		t.Fatal("create overwrote external file")
	}
	abort := runWriteTest(t, e, writeRequest("abort", "write_abort", map[string]any{"write_id": "begin"}), execprotocol.Completed)
	if abort.Write.Action != "aborted" {
		t.Fatal(abort)
	}
	runWriteTest(t, e, writeRequest("after-abort", "write_chunk", map[string]any{"write_id": "begin", "index": 1, "content": "x"}), execprotocol.Failed)
}

func TestChunkedWriteContextExpiryAndUnknownCommit(t *testing.T) {
	config := writeTestConfig(t)
	e := writeTestEngine(t, config)
	runWriteTest(t, e, writeRequest("begin", "write_begin", map[string]any{"path": "new"}), execprotocol.Completed)
	request := writeRequest("other-reset", "write_abort", map[string]any{"write_id": "begin"})
	request.WriteContext.ResetID = "new-reset"
	runWriteTest(t, e, request, execprotocol.Failed)
	request = writeRequest("other-thread", "write_abort", map[string]any{"write_id": "begin"})
	request.WriteContext.ThreadID = "other-thread"
	runWriteTest(t, e, request, execprotocol.Failed)
	runWriteTest(t, e, writeRequest("chunk", "write_chunk", map[string]any{"write_id": "begin", "index": 0, "content": "buffer"}), execprotocol.Completed)
	// A process can disappear after publication but before its typed receipt.
	unknown := writeRequest("uncertain", "write_commit", map[string]any{"write_id": "begin"})
	e.mu.Lock()
	encoded, _ := json.Marshal(unknown)
	op := &operation{record: record{Request: unknown, Hash: digest(encoded), State: execprotocol.Unknown, WriteAttempted: true, OutputExpired: true, CreatedAt: time.Now()}}
	e.operations[unknown.ID] = op
	err := e.save(&op.record)
	e.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	e = writeTestEngine(t, config)
	runWriteTest(t, e, writeRequest("repeat", "write_commit", map[string]any{"write_id": "begin"}), execprotocol.Unknown)
	if _, err := os.Stat(filepath.Join(config.WorkingDirectory, "new")); !os.IsNotExist(err) {
		t.Fatal("uncertain write was replayed")
	}
	// Model a commit that did publish before losing its acknowledgement. A
	// changed inode must not let a new begin bypass that uncertain session.
	if err := os.WriteFile(filepath.Join(config.WorkingDirectory, "new"), []byte("published"), 0644); err != nil {
		t.Fatal(err)
	}
	runWriteTest(t, e, writeRequest("bypass", "write_begin", map[string]any{"path": "new"}), execprotocol.Failed)
	runWriteTest(t, e, writeRequest("expired", "write_begin", map[string]any{"path": "expired"}), execprotocol.Completed)
	e.mu.Lock()
	e.operations["expired"].record.Write.At = time.Now().Add(-3 * time.Hour)
	e.mu.Unlock()
	runWriteTest(t, e, writeRequest("late", "write_chunk", map[string]any{"write_id": "expired", "index": 0, "content": "late"}), execprotocol.Failed)
}

func TestChunkedWriteSharedNewParentAndPrunedOutput(t *testing.T) {
	config := writeTestConfig(t)
	e := writeTestEngine(t, config)
	for _, name := range []string{"a", "b"} {
		runWriteTest(t, e, writeRequest(name, "write_begin", map[string]any{"path": "directory/" + name}), execprotocol.Completed)
		snapshot := runWriteTest(t, e, writeRequest(name+"-chunk", "write_chunk", map[string]any{"write_id": name, "index": 0, "content": name}), execprotocol.Completed)
		if err := e.Acknowledge("agent", snapshot.ID, snapshot.OutputBytes); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.Prune(time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a", "b"} {
		runWriteTest(t, e, writeRequest(name+"-commit", "write_commit", map[string]any{"write_id": name}), execprotocol.Completed)
		data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "directory", name))
		if err != nil || string(data) != name {
			t.Fatal(name, string(data), err)
		}
	}
}
