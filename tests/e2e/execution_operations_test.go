//go:build postgres

package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/juex-ai/juex/internal/entrypoints/executionhttp"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/connector"
	"github.com/juex-ai/juex/internal/execution/native"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func executionEventually(t *testing.T, f *executionFixture, device, id string, predicate func(execution.Operation) bool) execution.Operation {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	var operation execution.Operation
	for time.Now().Before(deadline) {
		var err error
		operation, err = f.execution.Operation(context.Background(), f.actor, f.tenant, f.agent.ID, device, id, 0, 256<<10)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(operation) {
			return operation
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("operation did not reach expected state", operation)
	return operation
}

func connectExecutionDevice(t *testing.T, f *executionFixture, device execution.Device, token string, engine *native.Engine) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	server := httptest.NewTLSServer(executionhttp.New(ctx, f.execution))
	done := make(chan error, 1)
	go func() {
		done <- connector.Run(ctx, connector.Config{URL: server.URL, Token: token, Environment: device.Environment, Engine: engine, HTTPClient: server.Client()})
	}()
	var stopped bool
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		server.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("device connector failed to stop")
		}
	}
	t.Cleanup(stop)
	return stop
}

func TestExecutionDurableQueueReconnectAndBinaryOutput(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, token := f.pairDevice(t)
	config := native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: device.ID, Grants: device.Ceiling}
	engine := openNative(t, config)
	request := nativeRequest(t, "long-disconnected-operation", "exec_command", native.CommandArguments{Command: "printf once >> counter; printf started; sleep 1; printf '\\377\\000a'"})
	request.AgentID = f.agent.ID
	operation, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0)
	if err != nil || operation.State != "waiting" {
		t.Fatal(operation, err)
	}
	if _, err := os.Stat(filepath.Join(config.WorkingDirectory, "counter")); !os.IsNotExist(err) {
		t.Fatal("offline queued command ran", err)
	}
	stop := connectExecutionDevice(t, f, device, token, engine)
	executionEventually(t, f, device.ID, request.ID, func(operation execution.Operation) bool {
		return bytes.Contains(operation.Snapshot.Output, []byte("started"))
	})
	stop()
	// Replace service and repository objects while the device process lives.
	f.executionStore = executionpg.New(f.pool)
	f.execution = &execution.Service{Store: f.executionStore, Authority: f.execution.Authority}
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err != nil {
		t.Fatal("durable submit retry", err)
	}
	connectExecutionDevice(t, f, device, token, engine)
	operation = executionEventually(t, f, device.ID, request.ID, func(operation execution.Operation) bool { return operation.Acknowledged })
	if operation.State != "completed" || !bytes.Equal(operation.Snapshot.Output, []byte{'s', 't', 'a', 'r', 't', 'e', 'd', 255, 0, 'a'}) || operation.ResultCursor != 10 {
		t.Fatal("byte cursor corrupted by JSON transport", operation)
	}
	data, err := os.ReadFile(filepath.Join(config.WorkingDirectory, "counter"))
	if err != nil || string(data) != "once" {
		t.Fatal("reconnect repeated side effect", string(data), err)
	}
	changed := request
	changed.Arguments = []byte(`{"command":"printf duplicate"}`)
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, changed, 0); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("reused durable identity", err)
	}
	part, err := f.execution.Operation(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID, 7, 2)
	if err != nil || !bytes.Equal(part.Snapshot.Output, []byte{255, 0}) || part.Snapshot.NextCursor != 9 {
		t.Fatal("byte output page", part, err)
	}
}

func TestExecutionOfflineCancellationWaitAndJournalFence(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	request := nativeRequest(t, "waiting-operation", "exec_command", native.CommandArguments{Command: "printf forbidden"})
	request.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.Extend(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID, 48*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.Cancel(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID); err != nil {
		t.Fatal(err)
	}
	operation, err := f.executionStore.Operation(ctx, device.ID, request.ID, 0, 100)
	if err != nil || operation.State != "cancelled" || !operation.Acknowledged {
		t.Fatal(operation, err)
	}
	request.ID = "expired-operation"
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.operations SET wait_until=clock_timestamp()-interval '1 second' WHERE id=$1`, request.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err = f.executionStore.Operation(ctx, device.ID, request.ID, 0, 100)
	if err != nil || operation.State != "failed" || !operation.Acknowledged {
		t.Fatal(operation, err)
	}
	request.ID = "ambiguous-dispatch"
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err != nil {
		t.Fatal(err)
	}
	journal := rand.Text()
	one, err := f.executionStore.Connect(ctx, device.ID, journal)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(ctx, device.ID, one.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	two, err := f.executionStore.Connect(ctx, device.ID, journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.Settle(ctx, device.ID, one.ConnectionEpoch, request.ID, execprotocol.Completed, ""); !errors.Is(err, execprotocol.ErrConflict) {
		t.Fatal("old connection wrote after replacement", err)
	}
	if err := f.executionStore.Touch(ctx, device.ID, two.ConnectionEpoch, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Connect(ctx, device.ID, rand.Text()); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("replaced journal accepted", err)
	}
	operation, err = f.executionStore.Operation(ctx, device.ID, request.ID, 0, 100)
	if err != nil || operation.State != "unknown" {
		t.Fatal("lost journal did not preserve uncertainty", operation, err)
	}
	if _, err := f.executionStore.Connect(ctx, device.ID, journal); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("quarantine bypassed by restoring journal", err)
	}
}

func TestExecutionRevocationReachesDisconnectedDevice(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, token := f.pairDevice(t)
	config := native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), WorkingDirectory: t.TempDir(), EnvironmentID: device.ID, Grants: device.Ceiling}
	engine := openNative(t, config)
	request := nativeRequest(t, "cancel-on-reconnect", "exec_command", native.CommandArguments{Command: "printf running; sleep 30"})
	request.AgentID = f.agent.ID
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err != nil {
		t.Fatal(err)
	}
	stop := connectExecutionDevice(t, f, device, token, engine)
	executionEventually(t, f, device.ID, request.ID, func(operation execution.Operation) bool {
		return operation.State == "running" && operation.ResultCursor > 0
	})
	stop()
	if err := f.execution.Revoke(ctx, f.actor, f.tenant, device.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.execution.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err := f.executionStore.Operation(ctx, device.ID, request.ID, 0, 100)
	if err != nil || !operation.CancelRequested || operation.State != "running" {
		t.Fatal("offline cancellation falsely reported stopped", operation, err)
	}
	request.ID = "new-after-revoke"
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("revoked device accepted work", err)
	}
	connectExecutionDevice(t, f, device, token, engine)
	operation = executionEventually(t, f, device.ID, "cancel-on-reconnect", func(operation execution.Operation) bool { return operation.Acknowledged })
	if operation.State != "cancelled" {
		t.Fatal(operation)
	}
}

func TestExecutionResultReservationsAndRetention(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t)
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	request := nativeRequest(t, "reserved-0", "exec_command", native.CommandArguments{Command: "true"})
	request.AgentID = f.agent.ID
	for index := range 64 {
		request.ID = fmt.Sprintf("reserved-%d", index)
		if _, err := f.executionStore.Enqueue(ctx, device, scope, request, time.Hour, false); err != nil {
			t.Fatal(index, err)
		}
	}
	if _, err := f.executionStore.Enqueue(ctx, device, scope, request, time.Hour, false); err != nil {
		t.Fatal("quota blocked an existing operation's receipt", err)
	}
	request.ID = "excess"
	if _, err := f.executionStore.Enqueue(ctx, device, scope, request, time.Hour, false); !errors.Is(err, execprotocol.ErrQuota) {
		t.Fatal("unbounded result reservations", err)
	}
	if err := f.executionStore.CancelOperation(ctx, device.ID, "reserved-0"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Enqueue(ctx, device, scope, request, time.Hour, false); err != nil {
		t.Fatal("cancelled pre-dispatch work retained a reservation", err)
	}
	connected, err := f.executionStore.Connect(ctx, device.ID, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: device.ID, ID: request.ID, AgentID: f.agent.ID, Kind: request.Kind, State: execprotocol.Completed, Output: []byte("retained"), NextCursor: 8, OutputBytes: 8}
	if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.operations SET updated_at=clock_timestamp()-interval '8 days' WHERE id=$1`, request.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.ExpireWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err := f.executionStore.Operation(ctx, device.ID, request.ID, 0, 100)
	if err != nil || operation.Snapshot.Text() != "retained" {
		t.Fatal("unacknowledged output rotated", operation, err)
	}
	if err := f.executionStore.Acknowledge(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.operations SET updated_at=clock_timestamp()-interval '8 days' WHERE id=$1`, request.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.ExpireWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err = f.executionStore.Operation(ctx, device.ID, request.ID, 8, 100)
	if err != nil || !operation.Snapshot.OutputExpired || len(operation.Snapshot.Output) != 0 {
		t.Fatal("expired output not explicit", operation, err)
	}
	if _, err := f.executionStore.Enqueue(ctx, device, scope, request, time.Hour, false); err != nil {
		t.Fatal("retention discarded deduplication identity", err)
	}
	request.ID = "empty-output"
	if _, err := f.executionStore.Enqueue(ctx, device, scope, request, time.Hour, false); err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	snapshot.ID = request.ID
	snapshot.Output = nil
	snapshot.OutputBytes = 0
	snapshot.NextCursor = 0
	if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
		t.Fatal("empty binary output", err)
	}
	if err := f.executionStore.Acknowledge(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionObservedOutputWaitsForRuntimeConsumption(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, _ := f.pairDevice(t, execprotocol.MCP)
	gateway := runtimeExecutionGateway(t, f)
	scope, err := f.execution.Authority.Agent(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	request := nativeRequest(t, "observed-notifications", "mcp_connect", native.MCPArguments{Command: "fixture"})
	request.AgentID = f.agent.ID
	fence := execprotocol.AuthorityFence{ActorEpoch: scope.ActorAuthorizationEpoch, MembershipEpoch: scope.MembershipExecutionEpoch, AgentEpoch: scope.AgentExecutionEpoch}
	if _, err := f.execution.SubmitFenced(ctx, f.actor, f.tenant, device.ID, request, time.Hour, fence); err != nil {
		t.Fatal(err)
	}
	connected, err := f.executionStore.Connect(ctx, device.ID, rand.Text())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	snapshot := execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: device.ID, ID: request.ID, AgentID: f.agent.ID, Kind: request.Kind, State: execprotocol.Completed, Output: []byte("retained"), NextCursor: 8, OutputBytes: 8}
	if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.Acknowledge(ctx, device.ID, connected.ConnectionEpoch, request.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.operations SET updated_at=clock_timestamp()-interval '8 days' WHERE id=$1`, request.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.ExpireWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err := f.executionStore.Operation(ctx, device.ID, request.ID, 0, 100)
	if err != nil || operation.Snapshot.Text() != "retained" {
		t.Fatal("unconsumed notifications expired", operation, err)
	}
	if err := gateway.Client.AcknowledgeOutput(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID, 9); !errors.Is(err, execprotocol.ErrInvalid) {
		t.Fatal("acknowledged nonexistent bytes", err)
	}
	if err := gateway.Client.AcknowledgeOutput(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID, 4); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.ExpireWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err = f.executionStore.Operation(ctx, device.ID, request.ID, 0, 100)
	if err != nil || operation.Snapshot.Text() != "retained" {
		t.Fatal("partial observer confirmation released remaining bytes", operation, err)
	}
	if err := gateway.Client.AcknowledgeOutput(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID, 8); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.ExpireWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err = f.executionStore.Operation(ctx, device.ID, request.ID, 0, 100)
	if err != nil || operation.Snapshot.Text() != "retained" {
		t.Fatal("retention did not start after consumer handoff", operation, err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE execution.operations SET observed_at=clock_timestamp()-interval '8 days' WHERE id=$1`, request.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.executionStore.ExpireWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	operation, err = f.executionStore.Operation(ctx, device.ID, request.ID, 8, 100)
	if err != nil || !operation.Snapshot.OutputExpired {
		t.Fatal("consumed output did not expire", operation, err)
	}
	if err := gateway.Client.AcknowledgeOutput(ctx, f.actor, f.tenant, f.agent.ID, device.ID, request.ID, 8); err != nil {
		t.Fatal("idempotent consumed confirmation", err)
	}
}

func TestExecutionIdleConnectionServicesDevicePings(t *testing.T) {
	f := executionDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	device, token := f.pairDevice(t)
	handler := executionhttp.New(ctx, f.execution)
	server := httptest.NewUnstartedServer(handler)
	server.Config.ReadTimeout = 200 * time.Millisecond
	server.Config.WriteTimeout = 200 * time.Millisecond
	server.StartTLS()
	defer server.Close()
	header := map[string][]string{"Authorization": {"Bearer " + token}}
	connection, _, err := websocket.Dial(ctx, server.URL+"/device/connect", &websocket.DialOptions{HTTPClient: server.Client(), HTTPHeader: header})
	if err != nil {
		t.Fatal(err)
	}
	defer connection.CloseNow()
	environment := device.Environment
	environment.JournalID = rand.Text()
	if err := wsjson.Write(ctx, connection, execprotocol.Envelope{Version: execprotocol.Version, Type: "hello", Environment: &environment}); err != nil {
		t.Fatal(err)
	}
	var welcome execprotocol.Envelope
	if err := wsjson.Read(ctx, connection, &welcome); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			var request execprotocol.Envelope
			if err := wsjson.Read(ctx, connection, &request); err != nil {
				return
			}
			if err := wsjson.Write(ctx, connection, execprotocol.Envelope{Version: execprotocol.Version, ID: request.ID, Type: "result"}); err != nil {
				return
			}
		}
	}()
	defer func() { cancel(); _ = connection.CloseNow(); <-readDone }()
	// Nothing is queued. Pings must work without a submit/query or grant edit.
	for range 3 {
		time.Sleep(250 * time.Millisecond)
		ping, stop := context.WithTimeout(ctx, time.Second)
		err := connection.Ping(ping)
		stop()
		if err != nil {
			t.Fatal("idle device heartbeat failed", err)
		}
	}
}
