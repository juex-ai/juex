//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
)

func observerFixture(t *testing.T, command string) (*executionFixture, execution.Device, managedruntime.ToolGateway, managedruntime.ObserverStart, string) {
	t.Helper()
	f := executionDatabase(t)
	ctx := context.Background()
	device, token := f.pairDevice(t)
	directory := t.TempDir()
	manifest := extensionpolicy.Manifest{ManifestVersion: 2, Name: "manual-observer", Version: "1", Observables: []extensionpolicy.ObservableResource{{CommandResource: extensionpolicy.CommandResource{ID: "watch", Command: []string{"/bin/sh", "-c", command}}, Options: execprotocol.ObservableOptions{Parser: execprotocol.ObservableParser{Type: "jsonl", ContentField: "message"}, Batch: execprotocol.ObservableBatch{IntervalSeconds: 1}}}}}
	writeExtensionManifest(t, directory, manifest)
	connectExecutionDevice(t, f, device, token, openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling}))
	adapter, _ := extensionManagement(t, f)
	receipt := inspectManagedExtension(t, f, adapter, device.ID, directory)
	binding := uuid.NewString()
	var err error
	f.agent, err = adapter.Configure(ctx, f.actor, f.tenant, f.agent.ID, binding, management.ExtensionChange{Version: f.agent.Version, Enabled: true, EnvironmentID: device.ID, InspectionID: receipt.OperationID, Resources: []string{"observable/watch"}})
	if err != nil {
		t.Fatal(err)
	}
	gateway := runtimeExecutionGateway(t, f)
	f.service.Tools = gateway
	request := managedruntime.ObserverStart{RequestID: uuid.NewString(), ThreadID: f.main.ID, BindingID: binding, ResourceID: "watch", Kind: "observable", AgentVersion: f.agent.Version, Revision: receipt.Catalog.Revision, Mode: "once"}
	return f, device, gateway, request, directory
}

func TestManualObserverStartReceiptAndStopBeforeAdmission(t *testing.T) {
	f, device, gateway, request, directory := observerFixture(t, `printf x >> starts; sleep 60`)
	ctx := context.Background()
	tables := []string{"runtime.threads", "runtime.observer_controls", "runtime.observation_sources", "execution.operations", "execution.environments"}
	before := statusRows(t, f.pool, tables...)
	for range 3 {
		page := managementCall[managedruntime.ObservationPage](t, f.client, "GET", f.base+"/observation-sources", f.origin, nil, 200)
		if len(page.Sources) != 0 || len(page.Controls) != 0 {
			t.Fatal(page)
		}
	}
	if !reflect.DeepEqual(before, statusRows(t, f.pool, tables...)) {
		t.Fatal("inspection mutated state")
	}
	// No model can be resolved: an explicit observer is not a fabricated model Turn.
	if _, err := f.pool.Exec(ctx, `UPDATE management.models SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	value, startErr := f.service.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, request)
	if startErr != nil {
		t.Fatal("StartObserver", startErr)
	}
	replay := managementCall[managedruntime.ObserverControl](t, f.client, "POST", f.base+"/observers", f.origin, request, 200)
	if replay.ID != value.ID || replay.SourceID != value.SourceID {
		t.Fatal("start identity changed", replay, value)
	}
	changed := request
	changed.Subscribe = true
	managementCall[any](t, f.client, "POST", f.base+"/observers", f.origin, changed, 409)
	managementCall[any](t, f.client, "POST", f.base+"/observation-sources/"+value.SourceID+"/stop", f.origin, map[string]any{}, 200)
	runRuntimeTools(t, f, gateway)
	runtimeEventually(t, func() bool {
		var state string
		err := f.pool.QueryRow(ctx, `SELECT state FROM runtime.observer_controls WHERE id=$1`, value.ID).Scan(&state)
		return err == nil && state == "cancelled"
	})
	if _, err := os.Stat(filepath.Join(directory, "starts")); !os.IsNotExist(err) {
		t.Fatal("stopped request executed", err)
	}
	var attempts, tools int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime.attempts),(SELECT count(*) FROM runtime.tools)`).Scan(&attempts, &tools); err != nil || attempts != 0 || tools != 0 {
		t.Fatal(attempts, tools, err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	var frozen execprotocol.Request
	if err := f.pool.QueryRow(ctx, `SELECT request FROM runtime.observer_controls WHERE id=$1`, value.ID).Scan(&frozen); err != nil {
		t.Fatal(err)
	}
	late, err := gateway.Submit(ctx, scope, device.ID, frozen)
	if err != nil || late.State != "cancelled" {
		t.Fatal("late admission bypassed stop", late, err)
	}
}

func TestManualObserverImmediateEventSurvivesRuntimeRestartAndStops(t *testing.T) {
	f, device, gateway, request, directory := observerFixture(t, `printf x >> starts; printf '%s\n' '{"message":"first event before model work"}'; sleep 60`)
	ctx := context.Background()
	request.Subscribe = true
	if _, err := f.pool.Exec(ctx, `UPDATE management.models SET enabled=false`); err != nil {
		t.Fatal(err)
	}
	value := managementCall[managedruntime.ObserverControl](t, f.client, "POST", f.base+"/observers", f.origin, request, 200)
	stop := runRuntimeTools(t, f, gateway)
	runtimeEventually(t, func() bool {
		var events int
		err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='observation'`).Scan(&events)
		return err == nil && events > 0
	})
	events := managementCall[managedruntime.ObservedEvents](t, f.client, "GET", f.base+"/observation-sources/"+value.SourceID+"/events", f.origin, nil, 200)
	if len(events.Items) == 0 || events.Items[0].Delivered != 1 {
		t.Fatal("first event missed initial subscription", events)
	}
	stop()
	runRuntimeTools(t, f, gateway)
	replay := managementCall[managedruntime.ObserverControl](t, f.client, "POST", f.base+"/observers", f.origin, request, 200)
	if replay.SourceID != value.SourceID {
		t.Fatal(replay)
	}
	started, err := os.ReadFile(filepath.Join(directory, "starts"))
	if err != nil || string(started) != "x" {
		t.Fatal("restart resubmitted source", string(started), err)
	}
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ObservedEvents(ctx, f.actor, f.tenant, other.ID, value.SourceID, ""); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("cross Agent source visible", err)
	}
	managementCall[any](t, f.client, "POST", f.base+"/observation-sources/"+value.SourceID+"/stop", f.origin, map[string]any{}, 200)
	runtimeEventually(t, func() bool {
		operation, err := f.executionStore.Operation(ctx, device.ID, value.SourceID, 0, 100)
		return err == nil && operation.State == "cancelled"
	})
	runtimeEventually(t, func() bool {
		var enabled int
		err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.subscriptions WHERE enabled AND operation_id=$1`, value.SourceID).Scan(&enabled)
		return err == nil && enabled == 0
	})
}

func TestContinuousObserverRestartsConfirmedExitAndStopsItsCurrentInstance(t *testing.T) {
	f, _, gateway, request, directory := observerFixture(t, `printf x >> starts; printf '%s\n' '{"message":"completed iteration"}'`)
	ctx := context.Background()
	request.Mode = "continuous"
	value := managementCall[managedruntime.ObserverControl](t, f.client, "POST", f.base+"/observers", f.origin, request, 200)
	runRuntimeTools(t, f, gateway)
	hooksEventually(t, func() bool { content, _ := os.ReadFile(filepath.Join(directory, "starts")); return len(content) >= 2 })
	// Stopping any historical instance stops the durable desired state, including a later instance.
	managementCall[any](t, f.client, "POST", f.base+"/observation-sources/"+value.SourceID+"/stop", f.origin, map[string]any{}, 200)
	hooksEventually(t, func() bool {
		var state string
		err := f.pool.QueryRow(ctx, `SELECT state FROM runtime.observer_controls WHERE id=$1 AND desired='stopped'`, value.ID).Scan(&state)
		return err == nil && (state == "completed" || state == "cancelled")
	})
	var infinity bool
	var count int64
	if err := f.pool.QueryRow(ctx, `SELECT next_check='infinity',attempt FROM runtime.observer_controls WHERE id=$1`, value.ID).Scan(&infinity, &count); err != nil || !infinity || count < 2 {
		t.Fatal(infinity, count, err)
	}
}

func TestManualSubscriptionBackfillsSampledOffsetRaceAndStopFencesClaimedDelivery(t *testing.T) {
	f, _, _, request, _ := observerFixture(t, `sleep 60`)
	ctx := context.Background()
	control, err := f.service.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	// The UI sampled offset zero. Before registration commits, the observer saves a newer fact.
	source, err := f.store.ClaimObservation(ctx, "registration-race")
	if err != nil {
		t.Fatal(err)
	}
	fact := managedruntime.Observation{ID: uuid.NewString(), Kind: "command.observation", EnvironmentID: source.EnvironmentID, OperationID: source.OperationID, Offset: 10, Data: json.RawMessage(`{"text":"between sample and subscription"}`), CreatedAt: time.Now().UTC()}
	if err = f.store.FinishObservation(ctx, source, managedruntime.ObservationBatch{Cursor: 10, Facts: []managedruntime.Observation{fact}}); err != nil {
		t.Fatal(err)
	}
	sub, err := f.store.SetSourceSubscription(ctx, scope, control.SourceID, f.main.ID, true, 0)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.store.SetSourceSubscription(ctx, scope, control.SourceID, f.main.ID, true, 10)
	if err != nil || replay.Generation != sub.Generation {
		t.Fatal(replay, err)
	}
	var count int
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.observation_deliveries WHERE subscription_id=$1 AND observation_id=$2`, sub.ID, fact.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("sampled event must be delivered exactly once", count, err)
	}
	delivery, err := f.store.ClaimObservationDelivery(ctx, "claimed-before-stop")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.service.StopObserver(ctx, f.actor, f.tenant, f.agent.ID, control.SourceID); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishObservationDelivery(ctx, delivery, true, nil); err != nil {
		t.Fatal(err)
	}
	if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'observation_id'=$1`, fact.ID).Scan(&count); err != nil || count != 0 {
		t.Fatal("stopped source queued claimed delivery", count, err)
	}
}

func TestContinuousObserverKeepsCurrentSubscriptionIntentAcrossInstances(t *testing.T) {
	f, _, _, request, _ := observerFixture(t, `sleep 60`)
	ctx := context.Background()
	request.Mode = "continuous"
	request.Subscribe = true
	control, err := f.service.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := f.service.Worker(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID, "subscription-worker", "Observer target")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.SetSourceSubscription(ctx, scope, control.SourceID, f.main.ID, false, 0); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.SetSourceSubscription(ctx, scope, control.SourceID, worker.ID, true, 0); err != nil {
		t.Fatal(err)
	}
	work, err := f.store.ClaimObserver(ctx, "next-instance")
	if err != nil {
		t.Fatal(err)
	}
	source, err := f.store.ClaimObservation(ctx, "drained-instance")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishObservation(ctx, source, managedruntime.ObservationBatch{Closed: true}); err != nil {
		t.Fatal(err)
	}
	if err = f.store.FinishObserver(ctx, work, managedruntime.ObserverOutcome{State: "completed", Admitted: true, Restart: true}); err != nil {
		t.Fatal(err)
	}
	var current string
	if err = f.pool.QueryRow(ctx, `SELECT source_id FROM runtime.observer_controls WHERE id=$1`, control.ID).Scan(&current); err != nil || current == control.SourceID {
		t.Fatal(current, err)
	}
	var targets []string
	rows, err := f.pool.Query(ctx, `SELECT thread_id FROM runtime.subscriptions WHERE operation_id=$1 AND enabled`, current)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var thread string
		if err = rows.Scan(&thread); err != nil {
			t.Fatal(err)
		}
		targets = append(targets, thread)
	}
	rows.Close()
	if !reflect.DeepEqual(targets, []string{worker.ID}) {
		t.Fatal("restart resurrected old or lost new subscription", targets)
	}
	// A UI action against a historical source resolves the current instance before changing its intent.
	currentSource, err := f.store.CurrentObserverSource(ctx, scope, control.SourceID)
	if err != nil || currentSource.ID != current {
		t.Fatal(currentSource, err)
	}
	if _, err = f.store.SetSourceSubscription(ctx, scope, control.SourceID, worker.ID, false, 0); !errors.Is(err, managedruntime.ErrConflict) {
		t.Fatal("stale subscription update crossed instance", err)
	}
	if err = f.service.Cancel(ctx, f.actor, f.tenant, f.agent.ID, worker.ID); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	if err = f.pool.QueryRow(ctx, `SELECT enabled FROM runtime.observer_subscriptions WHERE control_id=$1 AND thread_id=$2`, control.ID, worker.ID).Scan(&enabled); err != nil || enabled {
		t.Fatal("cancelled target retains persistent intent", enabled, err)
	}
}

func TestManualObserverRPCAndUninitializedReadBoundary(t *testing.T) {
	f, _, _, request, _ := observerFixture(t, `sleep 60`)
	ctx := context.Background()
	listener := platformListener(t)
	server, err := serverrpc.NewRuntime(listener, platformrpc.CredentialsAt(f.credentials, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(ctx) == nil })
	foreign, err := runtimerpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "execution"))
	if err != nil {
		t.Fatal(err)
	}
	// Runtime rejects Execution at the authenticated transport before its handlers run.
	if _, err = foreign.ObservationSources(ctx, f.actor, f.tenant, f.agent.ID, ""); !errors.Is(err, platformrpc.ErrUnavailable) {
		t.Fatal(err)
	}
	if _, err = foreign.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, request); !errors.Is(err, platformrpc.ErrUnavailable) {
		t.Fatal(err)
	}
	value, err := client.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := client.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, request)
	if err != nil || replay.ID != value.ID {
		t.Fatal(replay, err)
	}
	page, err := client.ObservationSources(ctx, f.actor, f.tenant, f.agent.ID, "")
	if err != nil || len(page.Sources) != 1 || page.Controls[0].RequestID != request.RequestID {
		t.Fatal(page, err)
	}
	events, err := client.ObservedEvents(ctx, f.actor, f.tenant, f.agent.ID, value.SourceID, "")
	if err != nil || len(events.Items) != 0 {
		t.Fatal(events, err)
	}
	if _, err = client.SetSourceSubscription(ctx, f.actor, f.tenant, f.agent.ID, value.SourceID, f.main.ID, false); err != nil {
		t.Fatal(err)
	}
	if err = client.StopObserver(ctx, f.actor, f.tenant, f.agent.ID, value.SourceID); err != nil {
		t.Fatal(err)
	}
	agent, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Never initialized"})
	if err != nil {
		t.Fatal(err)
	}
	before := statusRows(t, f.pool, "runtime.agents", "runtime.threads", "runtime.observer_controls", "execution.environments")
	for range 3 {
		page, err = client.ObservationSources(ctx, f.actor, f.tenant, agent.ID, "")
		if err != nil || len(page.Sources) != 0 || len(page.Targets) != 0 {
			t.Fatal(page, err)
		}
	}
	if !reflect.DeepEqual(before, statusRows(t, f.pool, "runtime.agents", "runtime.threads", "runtime.observer_controls", "execution.environments")) {
		t.Fatal("read initialized Runtime or Execution")
	}
}

func TestContinuousObserverStopsWhenItsSavedResourceIsReplaced(t *testing.T) {
	for _, change := range []string{"command", "directory"} {
		t.Run(change, func(t *testing.T) {
			f, device, gateway, request, directory := observerFixture(t, `printf x >> starts; sleep 60`)
			ctx := context.Background()
			request.Mode = "continuous"
			value, err := f.service.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			runRuntimeTools(t, f, gateway)
			runtimeEventually(t, func() bool {
				content, _ := os.ReadFile(filepath.Join(directory, "starts"))
				return string(content) == "x"
			})
			// Replace the inspected command without changing the selected resource identity.
			manifest := f.agent.Extensions[0].Catalog.Manifest
			target := directory
			if change == "command" {
				manifest.Observables[0].Command = []string{"/bin/sh", "-c", `printf y >> starts; sleep 60`}
			} else {
				target = t.TempDir()
			}
			writeExtensionManifest(t, target, manifest)
			adapter, _ := extensionManagement(t, f)
			receipt := inspectManagedExtension(t, f, adapter, device.ID, target)
			if _, err = adapter.Configure(ctx, f.actor, f.tenant, f.agent.ID, request.BindingID, management.ExtensionChange{Version: f.agent.Version, Enabled: true, EnvironmentID: device.ID, InspectionID: receipt.OperationID, Resources: []string{"observable/watch"}}); err != nil {
				t.Fatal(err)
			}
			hooksEventually(t, func() bool {
				var desired, state string
				err := f.pool.QueryRow(ctx, `SELECT desired,state FROM runtime.observer_controls WHERE id=$1`, value.ID).Scan(&desired, &state)
				return err == nil && desired == "stopped" && state == "cancelled"
			})
			content, err := os.ReadFile(filepath.Join(directory, "starts"))
			if err != nil || string(content) != "x" {
				t.Fatal("old desired state started a new resource revision", string(content), err)
			}
		})
	}
}

func TestContinuousObserverModelSubscriptionSharesPersistentIntent(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsubscribe", true: "subscribe"}[enabled], func(t *testing.T) {
			f, device, _, request, _ := observerFixture(t, `sleep 60`)
			ctx := context.Background()
			request.Mode, request.Subscribe = "continuous", !enabled
			control, err := f.service.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			name := "unsubscribe"
			args := map[string]any{}
			var subscription string
			if enabled {
				name = "subscribe"
				args = map[string]any{"kind": "command.observation", "environment_id": device.ID, "operation_id": control.SourceID}
			} else {
				if err = f.pool.QueryRow(ctx, `SELECT id FROM runtime.subscriptions WHERE operation_id=$1 AND enabled`, control.SourceID).Scan(&subscription); err != nil {
					t.Fatal(err)
				}
				args["subscription_id"] = subscription
			}
			_, tool := prepareRuntimeCall(t, f.managedRuntimeFixture, f.main.ID, "model-subscription-change", llm.Block{Type: llm.BlockToolUse, ToolUseID: "subscription-change", ToolName: name, Input: args})
			if enabled {
				_, err = f.store.ApplySubscription(ctx, tool, managedruntime.SubscriptionRequest{Kind: "command.observation", EnvironmentID: device.ID, OperationID: control.SourceID}, device.Version, 0, execprotocol.Shell, json.RawMessage(`{}`))
			} else {
				err = f.store.Unsubscribe(ctx, tool, subscription)
			}
			if err != nil {
				t.Fatal(err)
			}
			work, err := f.store.ClaimObserver(ctx, "after-model-intent")
			if err != nil {
				t.Fatal(err)
			}
			source, err := f.store.ClaimObservation(ctx, "model-intent-drain")
			if err != nil {
				t.Fatal(err)
			}
			if err = f.store.FinishObservation(ctx, source, managedruntime.ObservationBatch{Closed: true}); err != nil {
				t.Fatal(err)
			}
			if err = f.store.FinishObserver(ctx, work, managedruntime.ObserverOutcome{State: "completed", Admitted: true, Restart: true}); err != nil {
				t.Fatal(err)
			}
			var count int
			if err = f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.subscriptions s JOIN runtime.observer_controls c ON c.source_id::text=s.operation_id WHERE c.id=$1 AND s.thread_id=$2 AND s.enabled`, control.ID, f.main.ID).Scan(&count); err != nil || (count == 1) != enabled {
				t.Fatal("model intent lost on next instance", count, err)
			}
		})
	}
}

func TestContinuousObserverDoesNotRestartUnknownOrRestoredAuthority(t *testing.T) {
	for _, mode := range []string{"unknown", "revoke-and-restore"} {
		t.Run(mode, func(t *testing.T) {
			f, device, gateway, request, directory := observerFixture(t, `printf x >> starts; sleep 60`)
			ctx := context.Background()
			request.Mode = "continuous"
			value, err := f.service.StartObserver(ctx, f.actor, f.tenant, f.agent.ID, request)
			if err != nil {
				t.Fatal(err)
			}
			runRuntimeTools(t, f, gateway)
			runtimeEventually(t, func() bool { b, _ := os.ReadFile(filepath.Join(directory, "starts")); return string(b) == "x" })
			if mode == "unknown" {
				current, err := f.executionStore.Device(ctx, device.ID)
				if err != nil {
					t.Fatal(err)
				}
				if err = f.executionStore.Settle(ctx, device.ID, current.ConnectionEpoch, value.SourceID, execprotocol.Unknown, "test lost original execution outcome"); err != nil {
					t.Fatal(err)
				}
			} else {
				for _, enabled := range []bool{false, true} {
					configuration := f.agent.Configuration
					configuration.Modules = map[agentpolicy.Capability]bool{agentpolicy.Observations: enabled}
					f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, Configuration: &configuration})
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			hooksEventually(t, func() bool {
				var stopped bool
				return f.pool.QueryRow(ctx, `SELECT desired='stopped' AND next_check='infinity' AND attempt=1 FROM runtime.observer_controls WHERE id=$1`, value.ID).Scan(&stopped) == nil && stopped
			})
			if _, err = f.store.ClaimObserver(ctx, "must-not-restart"); !errors.Is(err, managedruntime.ErrNoWork) {
				t.Fatal("stopped control is still claimable", err)
			}
			b, err := os.ReadFile(filepath.Join(directory, "starts"))
			if err != nil || string(b) != "x" {
				t.Fatal("stale desired state restarted producer", string(b), err)
			}
		})
	}
}
