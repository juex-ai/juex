//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/entrypoints/executionhttp"
	"github.com/juex-ai/juex/internal/entrypoints/executorcli"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/hostservice"
	"github.com/juex-ai/juex/internal/execution/native"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func TestExecutionCLIWebPairingAndPrivateRPC(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
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
	if _, err := runtimeClient.Devices(ctx, f.actor, f.tenant, f.actor); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("runtime modified device administration", err)
	}
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Execution: client, PublicURL: f.origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	dashboard := httptest.NewServer(handler)
	defer dashboard.Close()
	deviceServer := httptest.NewServer(executionhttp.New(ctx, f.execution))
	defer deviceServer.Close()
	state := filepath.Join(t.TempDir(), "device")
	work := t.TempDir()
	pairCtx, cancelPair := context.WithCancel(ctx)
	defer cancelPair()
	in, approve := io.Pipe()
	defer in.Close()
	defer approve.Close()
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- executorcli.Execute(pairCtx, []string{"--state", state, "pair", "--server", deviceServer.URL, "--insecure-http", "--working-directory", work, "--name", "CLI laptop"}, in, &output, io.Discard)
	}()
	var pending struct {
		Request execution.PairRequest `json:"request"`
	}
	runtimeEventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(state, "pairing.json"))
		return err == nil && json.Unmarshal(data, &pending) == nil
	})
	base := dashboard.URL + "/api/tenants/" + f.tenant
	runtimeEventually(t, func() bool { _, err := f.executionStore.Pair(ctx, pending.Request.ID, ""); return err == nil })
	preview := managementCall[execution.Pairing](t, f.client, "GET", base+"/device-pairings/"+pending.Request.ID, f.origin, nil, 200)
	if preview.Name != "CLI laptop" {
		t.Fatal(preview)
	}
	managementCall[execution.Pairing](t, f.client, "POST", base+"/device-pairings/"+pending.Request.ID, f.origin, map[string]any{"grants": map[string][]execprotocol.Capability{f.agent.ID: {execprotocol.Files, execprotocol.Shell}}}, 200)
	devices := managementCall[[]execution.Device](t, f.client, "GET", base+"/users/"+f.actor+"/devices", f.origin, nil, 200)
	if len(devices) != 0 {
		t.Fatal("CLI local confirmation bypassed")
	}
	if _, err := io.WriteString(approve, "yes\n"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("CLI pairing did not finish")
	}
	if !strings.Contains(output.String(), "Tenant: Runtime") || !strings.Contains(output.String(), "Account: runtime@example.test") {
		t.Fatal("CLI did not explain approved identity")
	}
	info, err := os.Stat(filepath.Join(state, "enrollment.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("credential file permissions", err)
	}
	devices = managementCall[[]execution.Device](t, f.client, "GET", base+"/users/"+f.actor+"/devices", f.origin, nil, 200)
	if len(devices) != 1 {
		t.Fatal(devices)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- executorcli.Execute(runCtx, []string{"--state", state, "run"}, strings.NewReader(""), io.Discard, io.Discard)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-runDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("executor CLI shutdown timeout")
		}
		var status bytes.Buffer
		if err := executorcli.Execute(ctx, []string{"--state", state, "status"}, strings.NewReader(""), &status, io.Discard); err != nil {
			t.Error(err)
			return
		}
		var stopped hostservice.Status
		if err := json.Unmarshal(status.Bytes(), &stopped); err != nil || stopped.Running || stopped.State != "stopped" {
			t.Error("CLI did not confirm shutdown", stopped, err)
		}
	})
	runtimeEventually(t, func() bool {
		var output bytes.Buffer
		if err := executorcli.Execute(ctx, []string{"--state", state, "status"}, strings.NewReader(""), &output, io.Discard); err != nil {
			return false
		}
		var status hostservice.Status
		return json.Unmarshal(output.Bytes(), &status) == nil && status.Running && !status.Background && status.State == "online"
	})
	request := nativeRequest(t, "cli-command", "exec_command", native.CommandArguments{Command: "printf cli-success"})
	request.AgentID = f.agent.ID
	if _, err := runtimeClient.Submit(ctx, f.actor, f.tenant, devices[0].ID, request, 0); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("runtime bypassed authority fence", err)
	}
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	fence := execprotocol.AuthorityFence{ActorEpoch: scope.ActorAuthorizationEpoch, MembershipEpoch: scope.MembershipExecutionEpoch, AgentEpoch: scope.AgentExecutionEpoch}
	if _, err := runtimeClient.SubmitFenced(ctx, f.actor, f.tenant, devices[0].ID, request, 0, fence); err != nil {
		t.Fatal(err)
	}
	result := executionEventually(t, f, devices[0].ID, request.ID, func(operation execution.Operation) bool { return operation.Acknowledged })
	if result.Snapshot.Text() != "cli-success" {
		t.Fatal(result)
	}
	managementCall[any](t, f.client, "POST", base+"/devices/"+devices[0].ID+"/revoke", f.origin, struct{}{}, 200)
	if _, err := runtimeClient.SubmitFenced(ctx, f.actor, f.tenant, devices[0].ID, request, 0, fence); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("RPC ignored revoked device", err)
	}
	if _, err := client.Events(ctx, 100); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("management consumed execution events", err)
	}
	if err := client.AcknowledgeOutput(ctx, f.actor, f.tenant, f.agent.ID, devices[0].ID, request.ID, 0); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("management acknowledged Runtime consumption", err)
	}
	events, err := runtimeClient.Events(ctx, 100)
	if err != nil || len(events) == 0 {
		t.Fatal("runtime missing execution facts", events, err)
	}
	ids := make([]string, len(events))
	for i, event := range events {
		ids[i] = event.ID
	}
	if err := runtimeClient.AcknowledgeEvents(ctx, ids); err != nil {
		t.Fatal(err)
	}
}
