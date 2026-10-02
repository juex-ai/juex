//go:build postgres

package e2e

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/entrypoints/executionhttp"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
)

type hostedBackendProbe struct {
	starts, stops int
	credential    string
	fail          bool
}

func (p *hostedBackendProbe) Ensure(_ context.Context, _ execution.HostedResource, credential string) error {
	if p.fail {
		return errors.New("runtime unavailable")
	}
	p.starts++
	p.credential = credential
	return nil
}
func (p *hostedBackendProbe) Stop(context.Context, execution.HostedResource) error {
	p.stops++
	return nil
}

func hostedFixture(t *testing.T) (*executionFixture, *hostedBackendProbe, execprotocol.Environment) {
	t.Helper()
	f := executionDatabase(t)
	backend := &hostedBackendProbe{}
	f.execution.Hosted = &execution.HostedManager{Store: f.executionStore, Backend: backend, Authority: f.execution.Authority, Key: make([]byte, 32), Idle: time.Minute, StorageIdentity: "11111111-1111-4111-8111-111111111111"}
	environments, err := f.execution.Environments(context.Background(), f.actor, f.tenant, f.agent.ID)
	if err != nil || len(environments) != 1 || environments[0].Kind != "hosted" || environments[0].PermissionMode != "gvisor" {
		t.Fatal(environments, err)
	}
	return f, backend, environments[0]
}

func TestHostedLazyAdmissionLifecycleAndCredentialBoundary(t *testing.T) {
	f, backend, environment := hostedFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			envs, err := f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID)
			if err != nil || len(envs) != 1 || envs[0].ID != environment.ID {
				t.Error("concurrent environment allocation", envs, err)
			}
		})
	}
	wg.Wait()
	if err := f.execution.Hosted.Reconcile(ctx); err != nil || backend.starts != 0 {
		t.Fatal("listing started idle container", err, backend.starts)
	}
	if err := f.execution.Revoke(ctx, f.actor, f.tenant, environment.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("native administration changed hosted ownership", err)
	}
	request := nativeRequest(t, "hosted-durable-work", "exec_command", native.CommandArguments{Command: "printf once"})
	request.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, environment.ID, request, 0); err != nil {
		t.Fatal(err)
	}
	backend.fail = true
	if err := f.execution.Hosted.Reconcile(ctx); err == nil {
		t.Fatal("runtime failure silently accepted")
	}
	operation, err := f.executionStore.Operation(ctx, environment.ID, request.ID, 0, 100)
	if err != nil || operation.State != "waiting" {
		t.Fatal("infrastructure failure changed operation outcome", operation, err)
	}
	backend.fail = false
	if err := f.execution.Hosted.Reconcile(ctx); err != nil || backend.starts != 1 {
		t.Fatal("durable work did not start environment", err, backend.starts)
	}
	var provisioned bool
	var storage string
	var project, bytes, inodes int64
	if err := f.pool.QueryRow(ctx, `SELECT provisioned,storage_identity,project_id,workspace_bytes,workspace_inodes FROM execution.hosted WHERE environment_id=$1`, environment.ID).Scan(&provisioned, &storage, &project, &bytes, &inodes); err != nil || !provisioned || storage != f.execution.Hosted.StorageIdentity || project == 0 || bytes != 2<<30 || inodes != 131072 {
		t.Fatal("storage allocation not persisted", err, provisioned, storage, project, bytes, inodes)
	}
	originalStorage := f.execution.Hosted.StorageIdentity
	f.execution.Hosted.StorageIdentity = "22222222-2222-4222-8222-222222222222"
	if _, err := f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID); err == nil {
		t.Fatal("storage reconfiguration silently replaced workspace")
	}
	f.execution.Hosted.StorageIdentity = originalStorage
	device, err := f.executionStore.AuthenticateDevice(ctx, backend.credential)
	if err != nil || device.ID != environment.ID {
		t.Fatal("hosted enrollment cannot authenticate", device.ID, err)
	}
	// A stale idle timestamp cannot stop a queued/running process or MCP handle.
	if _, err := f.pool.Exec(ctx, `UPDATE execution.hosted SET last_activity=clock_timestamp()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.Hosted.Reconcile(ctx); err != nil || backend.stops != 0 {
		t.Fatal("unfinished work was reclaimed", err, backend.stops)
	}
	if err := f.execution.Cancel(ctx, f.actor, f.tenant, f.agent.ID, environment.ID, request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.hosted SET last_activity=clock_timestamp()-interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.Hosted.Reconcile(ctx); err != nil || backend.stops != 1 {
		t.Fatal("settled idle environment retained", err, backend.stops)
	}
	// A restart derives the same enrollment without storing plaintext in SQL.
	key := make([]byte, 32)
	f.execution.Hosted = &execution.HostedManager{Store: f.executionStore, Backend: backend, Authority: f.execution.Authority, Key: key, StorageIdentity: "11111111-1111-4111-8111-111111111111"}
	if _, err := f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID); err != nil {
		t.Fatal("restart lost environment", err)
	}
	key[0] = 1
	if _, err := f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID); err == nil {
		t.Fatal("incorrect key silently replaced enrollment")
	}
}

func TestHostedStopSerializesAgainstNewAdmission(t *testing.T) {
	f, _, environment := hostedFixture(t)
	ctx := context.Background()
	locked, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- f.executionStore.LockHosted(ctx, environment.ID, func(execution.HostedResource) (execution.HostedResult, error) {
			close(locked)
			<-release
			return execution.HostedResult{}, nil
		})
	}()
	<-locked
	request := nativeRequest(t, "concurrent-wake", "exec_command", native.CommandArguments{Command: "printf new"})
	request.AgentID = f.agent.ID
	admitted := make(chan error, 1)
	go func() {
		_, err := f.execution.Submit(ctx, f.actor, f.tenant, environment.ID, request, 0)
		admitted <- err
	}()
	select {
	case err := <-admitted:
		close(release)
		t.Fatal("new work raced container stop", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-admitted; err != nil {
		t.Fatal(err)
	}
	op, err := f.executionStore.Operation(ctx, environment.ID, request.ID, 0, 100)
	if err != nil || op.State != "waiting" {
		t.Fatal("admission lost after stop", op, err)
	}
}

func TestHostedRejoinRetainsWorkspaceButFencesOldWork(t *testing.T) {
	f, _, _ := hostedFixture(t)
	ctx := context.Background()
	member, err := f.directory.CreateUser(ctx, "hosted-owner@example.test")
	if err != nil {
		t.Fatal(err)
	}
	invite := func() {
		_, token, err := f.directory.Invite(ctx, f.actor, f.tenant, member.Email, management.Member, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.directory.AcceptInvitation(ctx, member.ID, token); err != nil {
			t.Fatal(err)
		}
	}
	invite()
	agent, err := f.directory.CreateAgent(ctx, member.ID, f.tenant, member.ID, management.AgentConfig{Name: "Hosted owner"})
	if err != nil {
		t.Fatal(err)
	}
	environments, err := f.execution.Environments(ctx, member.ID, f.tenant, agent.ID)
	if err != nil || len(environments) != 1 {
		t.Fatal(environments, err)
	}
	environment := environments[0]
	request := nativeRequest(t, "before-removal", "exec_command", native.CommandArguments{Command: "printf forbidden"})
	request.AgentID = agent.ID
	if _, err := f.execution.Submit(ctx, member.ID, f.tenant, environment.ID, request, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.ChangeMember(ctx, f.actor, f.tenant, member.ID, management.Member, management.Removed); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.execution.Environments(ctx, f.actor, f.tenant, agent.ID); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("admin bypassed removed owner", err)
	}
	invite()
	rejoined, err := f.execution.Environments(ctx, member.ID, f.tenant, agent.ID)
	if err != nil || len(rejoined) != 1 || rejoined[0].ID != environment.ID {
		t.Fatal("rejoin changed workspace identity", rejoined, err)
	}
	device, err := f.executionStore.Device(ctx, environment.ID)
	if err != nil || device.Status != "active" {
		t.Fatal(device, err)
	}
	grants, err := f.execution.EffectiveGrants(ctx, device)
	if err != nil || len(grants[agent.ID]) == 0 {
		t.Fatal("hosted authority not restored", grants, err)
	}
	op, err := f.executionStore.Operation(ctx, environment.ID, request.ID, 0, 100)
	if err != nil || op.State != "cancelled" {
		t.Fatal("rejoin revived old operation", op, err)
	}
	request.ID = "after-rejoin"
	if _, err := f.execution.Submit(ctx, member.ID, f.tenant, environment.ID, request, 0); err != nil {
		t.Fatal(err)
	}
}

func TestHostedEndpointExposesOnlyHostedConnections(t *testing.T) {
	f := executionDatabase(t)
	_, token := f.pairDevice(t)
	handler, err := executionhttp.New(context.Background(), f.execution, executionhttp.Options{})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler.HostedHandler())
	defer server.Close()
	for _, path := range []string{"/device/pair", "/api/session", "/health"} {
		response, err := http.Get(server.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusNotFound {
			t.Fatal(path, response.Status)
		}
	}
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/device/connect", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusForbidden {
		t.Fatal("native credential admitted through hosted endpoint", response.Status)
	}
}
