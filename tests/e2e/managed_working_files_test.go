//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func TestManagedWorkingFilesStayBoundAcrossDefaultChangeResetAndRestart(t *testing.T) {
	var mu sync.Mutex
	calls := map[string]int{}
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		var location managedruntime.WorkingFiles
		for _, message := range body.Messages {
			if message.Role != "system" {
				continue
			}
			text, _ := message.Content.(string)
			_, value, found := strings.Cut(text, "Thread working files (location data):\n")
			if !found {
				continue
			}
			line, _, _ := strings.Cut(value, "\n")
			if err := json.Unmarshal([]byte(line), &location); err != nil {
				t.Error(err)
			}
		}
		if location.Directory == "" {
			t.Error("working file guidance missing")
			streamManagedReply(w, "Missing")
			return
		}
		mu.Lock()
		calls[location.Directory]++
		count := calls[location.Directory]
		mu.Unlock()
		if count == 1 {
			streamManagedTool(w, "write", map[string]any{"environment_id": location.EnvironmentID, "path": filepath.Join(location.Directory, "draft/note.txt"), "content": location.Directory})
		} else if count == 3 {
			streamManagedTool(w, "read", map[string]any{"environment_id": location.EnvironmentID, "path": filepath.Join(location.Directory, "draft/note.txt")})
		} else {
			streamManagedReply(w, "Verified working files")
		}
	})
	ctx := context.Background()
	device, token := f.pairDevice(t)
	root := t.TempDir()
	if _, err := f.pool.Exec(ctx, `UPDATE execution.environments SET working_directory=$2 WHERE id=$1`, device.ID, root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID}); err != nil {
		t.Fatal(err)
	}
	connectExecutionDevice(t, f, device, token, openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "journal"), EnvironmentID: device.ID, WorkingDirectory: root, Grants: device.Ceiling}))
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var threads []managedruntime.Thread
	for _, name := range []string{"first", "second"} {
		worker, err := f.store.CreateWorker(ctx, scope, f.main.ID, name, name)
		if err != nil {
			t.Fatal(err)
		}
		threads = append(threads, worker)
	}
	stop := runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	for _, thread := range threads {
		f.submit(t, "write-"+thread.ID, thread.ID, "Keep a working file")
	}
	for _, thread := range threads {
		runtimeEventually(t, func() bool {
			return f.timeline(t, thread.ID).Thread.State == "idle" && f.timeline(t, thread.ID).Thread.PendingInputs == 0
		})
		var location managedruntime.WorkingFiles
		if err := f.pool.QueryRow(ctx, `SELECT location FROM runtime.thread_working_files WHERE thread_id=$1`, thread.ID).Scan(&location); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(root, ".juex-thread-work", thread.ID)
		if location.Directory != want || location.EnvironmentID != device.ID {
			t.Fatal("wrong Thread ownership", location)
		}
		if data, err := os.ReadFile(filepath.Join(want, "draft/note.txt")); err != nil || string(data) != want {
			t.Fatal("working file missing", err)
		}
	}
	stop()
	other, _ := f.pairDevice(t)
	binding, err := f.execution.DefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: other.ID, Version: binding.Version}); err != nil {
		t.Fatal(err)
	}
	for _, thread := range threads {
		if _, err := f.store.ResetContext(ctx, scope, thread.ID, "reset"); err != nil {
			t.Fatal(err)
		}
	}
	runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	for _, thread := range threads {
		f.submit(t, "read-"+thread.ID, thread.ID, "Read retained work from its original environment")
	}
	for _, thread := range threads {
		runtimeEventually(t, func() bool {
			return f.timeline(t, thread.ID).Thread.State == "idle" && f.timeline(t, thread.ID).Thread.PendingInputs == 0
		})
		mu.Lock()
		count := calls[filepath.Join(root, ".juex-thread-work", thread.ID)]
		mu.Unlock()
		if count != 4 {
			t.Fatal("new default replaced original working path", count)
		}
		var errors int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE t.thread_id=$1 AND (j.result->>'is_error')::boolean`, thread.ID).Scan(&errors); err != nil || errors != 0 {
			t.Fatal("working file tool failed", errors, err)
		}
	}
}
