//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func TestDefaultEnvironmentAPIAndCLIRespectAuthorityAndVersion(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	listener := platformListener(t)
	service, err := serverrpc.NewExecution(listener, platformrpc.CredentialsAt(f.credentials, "execution"), f.execution, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, service)
	client, err := executionrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	runtimeClient, err := executionrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeClient.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID}); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("Runtime changed user configuration", err)
	}
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	dashboard := httptest.NewUnstartedServer(nil)
	origin := "http://" + dashboard.Listener.Addr().String()
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Execution: client, PublicURL: origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	dashboard.Config.Handler = handler
	dashboard.Start()
	defer dashboard.Close()
	c := newManagedCLI(t, origin)
	cliValue[management.User](c, "runtime test password", "login", "--email", "runtime@example.test", "--password-stdin")
	initial := cliValue[execution.DefaultEnvironment](c, "", "agent", "environment", f.agent.ID)
	if initial.Version != 0 || initial.EnvironmentID != "" {
		t.Fatal(initial)
	}
	value := execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: "/Users/owner/agent-a"}
	encoded, _ := json.Marshal(value)
	value = cliValue[execution.DefaultEnvironment](c, string(encoded), "agent", "configure-environment", f.agent.ID)
	if value.Version != 1 || value.EnvironmentID != device.ID {
		t.Fatal(value)
	}
	if _, err := c.invoke(string(encoded), "agent", "configure-environment", f.agent.ID); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatal("lost concurrent edit", err)
	}
	envs := cliValue[[]execprotocol.Environment](c, "", "agent", "environments", f.agent.ID)
	if len(envs) != 1 || !envs[0].Default || envs[0].WorkingDirectory != value.WorkingDirectory || envs[0].Online {
		t.Fatal("offline default lost", envs)
	}
	// A fresh store/service instance reads the same binding, without a cache.
	restarted := &execution.Service{Authority: f.execution.Authority, Store: executionpg.New(f.pool)}
	if got, err := restarted.DefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID); err != nil || got != value {
		t.Fatal(got, err)
	}
	for _, invalid := range []execution.DefaultEnvironment{
		{EnvironmentID: device.ID, WorkingDirectory: "relative", Version: 1},
		{EnvironmentID: device.ID, WorkingDirectory: "/bad\x00path", Version: 1},
		{EnvironmentID: "not-an-id", Version: 1},
		{WorkingDirectory: "/orphan", Version: 1},
	} {
		if _, err := client.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, invalid); !errors.Is(err, execprotocol.ErrInvalid) {
			t.Fatal(invalid, err)
		}
	}
	second, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.SetDefaultEnvironment(ctx, f.actor, f.tenant, second.ID, execution.DefaultEnvironment{EnvironmentID: device.ID}); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("binding granted device access", err)
	}
	foreign, err := f.directory.CreateUser(ctx, "foreign-default@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.DefaultEnvironment(ctx, foreign.ID, f.tenant, f.agent.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("foreign owner read", err)
	}
	if _, err := client.SetDefaultEnvironment(ctx, foreign.ID, f.tenant, f.agent.ID, value); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("foreign owner write", err)
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			_, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, value)
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, execprotocol.ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatal("configuration version did not fence concurrent changes", successes.Load())
	}
}

type pausedDefaultProvisioning struct {
	execution.ManagedRepository
	reached, proceed chan struct{}
}

func (p pausedDefaultProvisioning) EnsureManaged(ctx context.Context, scope execution.Scope, candidate execution.ManagedResource) (execution.ManagedResource, error) {
	close(p.reached)
	select {
	case <-p.proceed:
		return p.ManagedRepository.EnsureManaged(ctx, scope, candidate)
	case <-ctx.Done():
		return execution.ManagedResource{}, ctx.Err()
	}
}

func TestDefaultEnvironmentConcurrentSelectionPreventsStaleHostedProvisioning(t *testing.T) {
	f := executionDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	device, _ := f.pairDevice(t)
	provisioning := pausedDefaultProvisioning{ManagedRepository: f.executionStore, reached: make(chan struct{}), proceed: make(chan struct{})}
	f.execution.Managed = &execution.ManagedManager{Store: provisioning, Backend: &hostedBackendProbe{}, Authority: f.execution.Authority, Key: make([]byte, 32)}
	type result struct {
		environments []execprotocol.Environment
		err          error
	}
	done := make(chan result, 1)
	go func() {
		values, err := f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID)
		done <- result{values, err}
	}()
	select {
	case <-provisioning.reached:
	case <-ctx.Done():
		t.Fatal("environment listing did not reach provisioning")
	}
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID}); err != nil {
		t.Fatal(err)
	}
	close(provisioning.proceed)
	got := <-done
	if got.err != nil || len(got.environments) != 1 || got.environments[0].ID != device.ID || !got.environments[0].Default {
		t.Fatal(got)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.managed_environments`).Scan(&count); err != nil || count != 0 {
		t.Fatal("stale list provisioned unused Hosted", count, err)
	}
}

func TestDefaultEnvironmentNativeSuppressesHostedAndNeverFallsBack(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	binding, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: "/Users/owner/project"})
	if err != nil {
		t.Fatal(err)
	}
	backend := &hostedBackendProbe{}
	f.execution.Managed = &execution.ManagedManager{Store: f.executionStore, Backend: backend, Authority: f.execution.Authority, Key: make([]byte, 32)}
	envs, err := f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil || len(envs) != 1 || !envs[0].Default {
		t.Fatal(envs, err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM execution.managed_environments`).Scan(&count); err != nil || count != 0 {
		t.Fatal("native binding allocated hosted workspace", count, err)
	}
	if err := f.execution.Revoke(ctx, f.actor, f.tenant, device.ID); err != nil {
		t.Fatal(err)
	}
	envs, err = f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil || len(envs) != 0 {
		t.Fatal("revoked default silently replaced", envs, err)
	}
	if _, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, binding); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("revoked device reselected", err)
	}
	reset, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{Version: binding.Version})
	if err != nil || reset.EnvironmentID != "" || reset.Version != 2 {
		t.Fatal(reset, err)
	}
	envs, err = f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil || len(envs) != 1 || envs[0].Kind != "hosted" || !envs[0].Default {
		t.Fatal("explicit reset did not restore deployment default", envs, err)
	}
	// Erasing Agent metadata must remove the directory binding without touching
	// native files; existing operation and Hosted cleanup are separate stages.
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.executionStore.Purge(ctx, lifecycle.Request{Phase: lifecycle.Erase, Target: lifecycle.Target{ID: uuid.NewString(), TenantID: f.tenant, UserID: f.actor, FleetID: scope.FleetID, AgentIDs: []string{f.agent.ID}}})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := f.executionStore.DefaultEnvironment(ctx, scope); err != nil || got.Version != 0 {
		t.Fatal("purged binding retained", got, err)
	}
}
