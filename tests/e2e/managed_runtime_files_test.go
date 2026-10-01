//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/blob"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func TestManagedRuntimeFileTransferWaitsWithoutModelSpinAndResumes(t *testing.T) {
	var deviceID string
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		messages, _ := json.Marshal(body["messages"])
		if calls.Add(1) == 1 {
			tools, _ := json.Marshal(body["tools"])
			if !strings.Contains(string(tools), "publish_file") {
				t.Error("file tool not advertised")
			}
			streamManagedTool(w, "publish_file", map[string]any{"source": map[string]any{"environment_id": deviceID, "path": "source.bin"}, "visibility": "fleet"})
			return
		}
		if !strings.Contains(string(messages), "artifact_id") || !strings.Contains(string(messages), "completed") {
			t.Error("missing published artifact result", string(messages))
		}
		if strings.Contains(string(messages), "binary-payload-never-in-model") {
			t.Error("file bytes entered model context")
		}
		streamManagedReply(w, "Published the file")
	})
	objects, err := blob.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	f.execution.Blobs = &execution.ArtifactManager{Store: f.executionStore, Objects: objects, Capacity: 32 << 20}
	f.execution.Transfers = f.executionStore
	device, token := f.pairDevice(t)
	deviceID = device.ID
	gateway := runtimeExecutionGateway(t, f)
	runner, err := managedruntime.NewRunner(f.store, f.authority, managedruntime.RunnerConfig{Tools: gateway, Files: gateway, Concurrency: 1, PollInterval: 20 * time.Millisecond, AuthorityInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	f.submit(t, "publish-file", f.main.ID, "Publish the file from my device for another Agent")
	runtimeEventually(t, func() bool {
		var count int
		return f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.tools WHERE state='waiting' AND next_check='infinity'`).Scan(&count) == nil && count == 1
	})
	if calls.Load() != 1 {
		t.Fatal("offline transfer consumed another model request")
	}
	config := native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: device.ID, Grants: device.Ceiling}
	if err := os.WriteFile(filepath.Join(config.WorkingDirectory, "source.bin"), []byte("binary-payload-never-in-model\x00\xff"), 0600); err != nil {
		t.Fatal(err)
	}
	connectExecutionDevice(t, f, device, token, openNative(t, config))
	runtimeEventually(t, func() bool {
		if err := f.execution.Reconcile(ctx); err != nil {
			t.Error(err)
			return false
		}
		return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle"
	})
	var transfers, operations int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.transfers`).Scan(&transfers); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if transfers != 1 || operations != 1 {
		t.Fatal("transfer replayed", transfers, operations)
	}
	assertRuntimeTranscript(t, f)
}

func TestManagedRuntimeTransferDestinationResumeSuppressesWaitNotice(t *testing.T) {
	var sourceID, targetID string
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		streamManagedTool(w, "copy_file", map[string]any{"source": map[string]any{"environment_id": sourceID, "path": "source.bin"}, "target": map[string]any{"environment_id": targetID, "path": "target.bin"}})
	})
	ctx := context.Background()
	objects, err := blob.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	f.execution.Blobs = &execution.ArtifactManager{Store: f.executionStore, Objects: objects, Capacity: 32 << 20}
	f.execution.Transfers = f.executionStore
	source, token := f.pairDevice(t)
	target, _ := f.pairDevice(t)
	sourceID, targetID = source.ID, target.ID
	gateway := runtimeExecutionGateway(t, f)
	runRuntimeTools(t, f, gateway)
	f.submit(t, "copy", f.main.ID, "Copy between my devices")
	runtimeEventually(t, func() bool {
		var count int
		return f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.tools WHERE waiting_reason='environment' AND next_check='infinity'`).Scan(&count) == nil && count == 1
	})
	config := native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: source.ID, Grants: source.Ceiling}
	if err := os.WriteFile(filepath.Join(config.WorkingDirectory, "source.bin"), []byte("copy data"), 0600); err != nil {
		t.Fatal(err)
	}
	connectExecutionDevice(t, f, source, token, openNative(t, config))
	var transfer execution.Transfer
	runtimeEventually(t, func() bool {
		if err := f.execution.Reconcile(ctx); err != nil {
			t.Error(err)
			return false
		}
		values, err := f.execution.ListTransfers(ctx, f.actor, f.tenant, f.agent.ID, "", 10)
		if err != nil || len(values) != 1 || values[0].ArtifactID == "" {
			return false
		}
		transfer = values[0]
		var count int
		return f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.tools WHERE waiting_reason='environment' AND next_check='infinity'`).Scan(&count) == nil && count == 1
	})
	if transfer.State != execprotocol.Accepted {
		t.Fatal(transfer)
	}
	// Simulate a slow destination import after reconnect. The committed operation
	// event must refresh Runtime while the aggregate transfer remains accepted.
	if _, err := f.pool.Exec(ctx, `UPDATE execution.operations SET state='running',snapshot=jsonb_set(snapshot,'{state}','"running"') WHERE environment_id=$1 AND id=$2`, target.ID, transfer.FileOperation(false, nil).ID); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool {
		var count int
		return f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.tools WHERE state='waiting' AND waiting_reason='execution' AND next_check='infinity'`).Scan(&count) == nil && count == 1
	})
	if _, err := f.pool.Exec(ctx, `UPDATE runtime.notification_outbox SET not_before='-infinity'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ClaimNotification(ctx); !errors.Is(err, managedruntime.ErrNoWork) {
		t.Fatal("recovered destination generated an obsolete device reminder", err)
	}
	if calls.Load() != 1 {
		t.Fatal("transfer progress woke the model", calls.Load())
	}
}
