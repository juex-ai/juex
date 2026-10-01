//go:build postgres

package e2e

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/juex-ai/juex/internal/app/managed"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/maintenance"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
)

func maintenanceGate(t *testing.T) (maintenance.Gate, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "admission.lock"), nil, 0644); err != nil {
		t.Fatal(err)
	}
	g, err := maintenance.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return g, dir
}
func beginMaintenance(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "draining"), []byte("test maintenance"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestManagedMaintenanceDrainsModelWithoutCancellingAndHoldsQueuedInput(t *testing.T) {
	gate, dir := maintenanceGate(t)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var calls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				t.Error("maintenance cancelled an admitted provider request")
				return
			}
		}
		streamManagedReply(w, "completed normally")
	})
	runner, err := managedruntime.NewRunner(f.store, f.authority, managedruntime.RunnerConfig{Admission: gate.Enter, PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); runner.Run(ctx) }()
	defer func() { cancel(); <-done }()
	f.submit(t, "before-maintenance", f.main.ID, "first")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("provider never started")
	}
	beginMaintenance(t, dir)
	if release, err := gate.Exclusive(); err == nil {
		release()
		t.Fatal("in-flight provider did not retain maintenance barrier")
	}
	f.submit(t, "queued-before-restore", f.main.ID, "second")
	once.Do(func() { close(release) })
	runtimeEventually(t, func() bool {
		release, err := gate.Exclusive()
		if err != nil {
			return false
		}
		release()
		return true
	})
	if calls.Load() != 1 {
		t.Fatal("queued input bypassed maintenance")
	}
	var state string
	if err := f.pool.QueryRow(context.Background(), `SELECT state FROM runtime.inputs WHERE request_id='queued-before-restore'`).Scan(&state); err != nil || state != "queued" {
		t.Fatal(state, err)
	}
	if err := os.Remove(filepath.Join(dir, "draining")); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return calls.Load() == 2 })
}

func TestManagedMaintenanceFencesLateAdmissionAndPreservesDeviceReceipts(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, token := f.pairDevice(t)
	gate, dir := maintenanceGate(t)
	f.execution.Admission = gate.Enter
	config := native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: device.ID, Grants: device.Ceiling}
	connectExecutionDevice(t, f, device, token, openNative(t, config))
	running := nativeRequest(t, "maintenance-running", "exec_command", native.CommandArguments{Command: "printf started; sleep 1; printf finished"})
	running.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, running, 0); err != nil {
		t.Fatal(err)
	}
	executionEventually(t, f, device.ID, running.ID, func(o execution.Operation) bool { return o.State == "running" })
	beginMaintenance(t, dir)
	report, err := executionpg.MaintenanceReport(ctx, f.pool)
	if err != nil || report.Ready() {
		t.Fatal("active device command hidden", report, err)
	}
	late := nativeRequest(t, "maintenance-late", "exec_command", native.CommandArguments{Command: "printf forbidden > late"})
	late.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, late, 0); !errors.Is(err, maintenance.ErrDraining) {
		t.Fatal("late admission was not retryable", err)
	}
	if _, err := f.executionStore.Operation(ctx, device.ID, late.ID, 0, 100); !errors.Is(err, execprotocol.ErrNotFound) {
		t.Fatal("late identity admitted", err)
	}
	executionEventually(t, f, device.ID, running.ID, func(o execution.Operation) bool { return o.Acknowledged && o.State == "completed" })
	if err := f.execution.Cancel(ctx, f.actor, f.tenant, f.agent.ID, device.ID, running.ID); err != nil {
		t.Fatal(err)
	}
	if err := memorypg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	if err := calendarpg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	address, err := url.Parse(f.pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	address.Path = "/" + f.pool.Config().ConnConfig.Database
	reports, err := managed.InspectMaintenance(ctx, address.String(), false)
	if err != nil || len(reports) != 5 {
		t.Fatal(reports, err)
	}
	for _, r := range reports {
		if !r.Ready() {
			t.Fatal(r)
		}
	}
	if _, err := managed.InspectMaintenance(ctx, address.String(), true); err == nil {
		t.Fatal("offline check accepted live service connections")
	}
	if err := os.Remove(filepath.Join(dir, "draining")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, late, 0); err != nil {
		t.Fatal(err)
	}
	executionEventually(t, f, device.ID, late.ID, func(o execution.Operation) bool { return o.Acknowledged })
}

func TestManagedMaintenanceOfflineAuthorityRecovery(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	_, dir := maintenanceGate(t)
	beginMaintenance(t, dir)
	config := f.pool.Config()
	address, err := url.Parse(config.ConnString())
	if err != nil {
		t.Fatal(err)
	}
	address.Path = "/" + config.ConnConfig.Database
	if err := managed.RevokeRecoveredAuthority(ctx, address.String(), dir, "", "", device.ID); err == nil {
		t.Fatal("offline recovery accepted live database clients")
	}
	f.pool.Close()
	if err := managed.RevokeRecoveredAuthority(ctx, address.String(), dir, "", "", device.ID); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	var state string
	var grants int
	if err := pool.QueryRow(ctx, `SELECT status,(SELECT count(*) FROM jsonb_object_keys(grants)) FROM execution.environments WHERE id=$1`, device.ID).Scan(&state, &grants); err != nil {
		t.Fatal(err)
	}
	if state != "revoked" || grants != 0 {
		t.Fatal("restored device authorization retained", state, grants)
	}
	var audit int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM execution.audit WHERE environment_id=$1 AND action='recovery.device_revoked' AND actor_id IS NULL`, device.ID).Scan(&audit); err != nil || audit != 1 {
		t.Fatal("operator revocation missing audit", audit, err)
	}
	pool.Close()
	if err := managed.RevokeRecoveredAuthority(ctx, address.String(), dir, f.tenant, f.actor, ""); !errors.Is(err, management.ErrLastAdmin) {
		t.Fatal("last admin invariant lost", err)
	}
}
