//go:build postgres

package e2e

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/management"
)

type hostLifecycleProbe struct {
	root                  string
	starts, stops, purges int
}

func (p *hostLifecycleProbe) Resource(id string) (execution.ManagedResource, error) {
	return execution.ManagedResource{EnvironmentID: id, Backend: "host", OS: runtime.GOOS,
		WorkingDirectory: filepath.Join(p.root, id, "workspace"), HomeDirectory: filepath.Join(p.root, id, "home")}, nil
}
func (p *hostLifecycleProbe) Ensure(context.Context, execution.ManagedResource, string) error {
	p.starts++
	return nil
}
func (p *hostLifecycleProbe) Stop(context.Context, execution.ManagedResource) error {
	p.stops++
	return nil
}
func (p *hostLifecycleProbe) Purge(context.Context, execution.ManagedResource) error {
	p.purges++
	return nil
}

func TestManagedHostAllocatesStableAgentOwnedDefaults(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	backend := &hostLifecycleProbe{root: t.TempDir()}
	f.execution.Managed = &execution.ManagedManager{Store: f.executionStore, Backend: backend, Authority: f.execution.Authority, Key: make([]byte, 32)}
	agent2, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Second Host"})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, agent := range []string{f.agent.ID, agent2.ID} {
		envs, err := f.execution.Environments(ctx, f.actor, f.tenant, agent)
		if err != nil || len(envs) != 1 || !envs[0].Default || envs[0].Kind != "native" || envs[0].PermissionMode != "current_os_user" {
			t.Fatal("Host default unavailable", envs, err)
		}
		ids[agent] = envs[0].ID
		if envs[0].WorkingDirectory != filepath.Join(backend.root, envs[0].ID, "workspace") {
			t.Fatal(envs)
		}
	}
	if ids[f.agent.ID] == ids[agent2.ID] {
		t.Fatal("Agents share a managed environment")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			envs, err := f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID)
			if err != nil || len(envs) != 1 || envs[0].ID != ids[f.agent.ID] {
				t.Error("concurrent allocation changed identity", envs, err)
			}
		})
	}
	wg.Wait()
	devices, err := f.execution.Devices(ctx, f.actor, f.tenant, f.actor)
	if err != nil || len(devices) != 0 {
		t.Fatal("managed Host exposed as paired device", devices, err)
	}
	if err := f.execution.Revoke(ctx, f.actor, f.tenant, ids[f.agent.ID]); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("external-device API changed managed ownership", err)
	}
	backend.root = t.TempDir()
	if _, err := f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID); err == nil {
		t.Fatal("Host root changed without explicit migration")
	}
}

func TestManagedHostUnknownOperationPreventsDestructivePurge(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	backend := &hostLifecycleProbe{root: t.TempDir()}
	f.execution.Managed = &execution.ManagedManager{Store: f.executionStore, Backend: backend, Authority: f.execution.Authority, Key: make([]byte, 32)}
	envs, err := f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil || len(envs) != 1 {
		t.Fatal(envs, err)
	}
	request := nativeRequest(t, "host-unknown", "exec_command", map[string]any{"command": "external side effect"})
	request.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, envs[0].ID, request, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.operations SET state='unknown',snapshot=jsonb_set(snapshot,'{state}','"unknown"') WHERE id=$1`, request.ID); err != nil {
		t.Fatal(err)
	}
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	r := lifecycle.Request{Target: lifecycle.Target{ID: uuid.NewString(), TenantID: f.tenant, UserID: f.actor, FleetID: scope.FleetID, AgentIDs: []string{f.agent.ID}}, Phase: lifecycle.Erase}
	if _, err := f.executionStore.Purge(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.Managed.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if backend.stops != 0 || backend.starts != 1 {
		t.Fatal("Host journal was stopped instead of reconnecting for confirmation", backend.stops, backend.starts)
	}
	receipt, err := f.executionStore.Purge(ctx, r)
	if err != nil || receipt.DataRemoved || receipt.Unconfirmed != 1 || backend.purges != 0 {
		t.Fatal("Host stop falsely proved process destruction", receipt, backend.purges, err)
	}
	op, err := f.executionStore.Operation(ctx, envs[0].ID, request.ID, 0, 1)
	if err != nil || op.State != "unknown" || op.Acknowledged {
		t.Fatal("unknown outcome was settled", op, err)
	}
}
