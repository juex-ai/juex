//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/blob"
	"github.com/juex-ai/juex/internal/execution/native"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func extensionManagement(t *testing.T, f *executionFixture) (managed.ManagementExtensions, string) {
	t.Helper()
	listener := platformListener(t)
	server, err := serverrpc.NewExecution(listener, platformrpc.CredentialsAt(f.credentials, "execution"), f.execution, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	client, err := executionrpc.NewClient(listener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "management"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return client.Health(context.Background()) == nil })
	adapter := managed.ManagementExtensions{Directory: f.directory, Execution: client}
	auth, err := managementpg.NewAuth(f.directory)
	if err != nil {
		t.Fatal(err)
	}
	web := httptest.NewUnstartedServer(nil)
	origin := "http://" + web.Listener.Addr().String()
	handler, err := managementhttp.New(managementhttp.Options{Auth: auth, Directory: f.directory, Extensions: adapter, PublicURL: origin, InsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	web.Config.Handler = handler
	web.Start()
	t.Cleanup(web.Close)
	managementCall[management.Session](t, f.client, "POST", origin+"/api/auth/login", origin, map[string]string{"email": "runtime@example.test", "password": "runtime test password"}, 200)
	return adapter, origin
}
func writeExtensionManifest(t *testing.T, directory string, manifest extensionpolicy.Manifest) {
	t.Helper()
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "juex.extension.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
func inspectManagedExtension(t *testing.T, f *executionFixture, adapter managed.ManagementExtensions, device, directory string) management.ExtensionInspection {
	t.Helper()
	ctx := context.Background()
	receipt, err := adapter.Inspect(ctx, f.actor, f.tenant, f.agent.ID, management.ExtensionInspectionRequest{RequestID: uuid.NewString(), EnvironmentID: device, Directory: directory})
	if err != nil {
		t.Fatal(err)
	}
	hooksEventually(t, func() bool {
		var err error
		receipt, err = adapter.Inspection(ctx, f.actor, f.tenant, f.agent.ID, device, receipt.OperationID)
		return err == nil && receipt.State == "completed"
	})
	return receipt
}
func TestManagementExtensionInspectionReceiptAndRevocation(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, token := f.pairDevice(t)
	adapter, origin := extensionManagement(t, f)
	base := origin + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID
	directory := t.TempDir()
	manifest := extensionpolicy.Manifest{ManifestVersion: 2, Name: "example", Version: "1.0", Skills: []extensionpolicy.SkillResource{{ID: "guide", Path: "SKILL.md"}}, Hooks: []hookpolicy.Declaration{{ID: "input", Enabled: true, Required: true, Events: []hookpolicy.Event{hookpolicy.UserPromptSubmit}, Command: []string{"/bin/true"}}}}
	writeExtensionManifest(t, directory, manifest)
	frozen := strings.Repeat("frozen skill\n", 5000)
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte(frozen), 0600); err != nil {
		t.Fatal(err)
	}
	request := management.ExtensionInspectionRequest{RequestID: uuid.NewString(), EnvironmentID: device.ID, Directory: directory}
	receipt := managementCall[management.ExtensionInspection](t, f.client, "POST", base+"/extension-inspections", origin, request, 200)
	managementCall[any](t, managementClient(t), "POST", base+"/extension-inspections", origin, request, 401)
	connectExecutionDevice(t, f, device, token, openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling}))
	hooksEventually(t, func() bool {
		var err error
		receipt, err = adapter.Inspection(ctx, f.actor, f.tenant, f.agent.ID, device.ID, receipt.OperationID)
		return err == nil && receipt.State == "completed"
	})
	receipt = managementCall[management.ExtensionInspection](t, f.client, "GET", base+"/extension-inspections/"+device.ID+"/"+receipt.OperationID, origin, nil, 200)
	if receipt.Catalog == nil || receipt.Catalog.Skills[0].Content != frozen {
		t.Fatal("inspection did not capture full skill")
	}
	if err := os.WriteFile(filepath.Join(directory, "SKILL.md"), []byte("changed on device"), 0600); err != nil {
		t.Fatal(err)
	}
	binding := uuid.NewString()
	change := management.ExtensionChange{Version: f.agent.Version, Enabled: true, EnvironmentID: device.ID, InspectionID: receipt.OperationID, Resources: []string{"skill/guide", "hook/input"}}
	f.agent = managementCall[management.Agent](t, f.client, "PUT", base+"/extensions/"+binding, origin, change, 200)
	if f.agent.Extensions[0].Catalog.Skills[0].Content != frozen {
		t.Fatal("saved mutable disk state instead of receipt")
	}
	managementCall[any](t, f.client, "PUT", base+"/extensions/"+binding, origin, change, 409)
	managementCall[any](t, f.client, "PUT", base+"/extensions/"+binding, origin, map[string]any{"version": f.agent.Version, "catalog": receipt.Catalog}, 400)
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	managementCall[any](t, f.client, "GET", origin+"/api/tenants/"+f.tenant+"/agents/"+other.ID+"/extension-inspections/"+device.ID+"/"+receipt.OperationID, origin, nil, 403)
	prior, err := f.directory.ReadAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	previousEpoch := prior.Agent.ExecutionEpoch
	manifest.Hooks[0].Enabled = false
	writeExtensionManifest(t, directory, manifest)
	refreshed := inspectManagedExtension(t, f, adapter, device.ID, directory)
	change.Version, change.InspectionID = f.agent.Version, refreshed.OperationID
	f.agent = managementCall[management.Agent](t, f.client, "PUT", base+"/extensions/"+binding, origin, change, 200)
	current, err := f.directory.ReadAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Agent.ExecutionEpoch <= previousEpoch {
		t.Fatal("manifest hook disable did not revoke frozen work")
	}
	if _, err := f.execution.Restrict(ctx, f.actor, f.tenant, device.ID, device.Version, map[string][]execprotocol.Capability{f.agent.ID: {execprotocol.Shell}}); err != nil {
		t.Fatal(err)
	}
	change.Version = f.agent.Version
	managementCall[any](t, f.client, "PUT", base+"/extensions/"+binding, origin, change, 403)
}

func TestManagedExtensionSkillsCommandsObserversAndAttachments(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	binding := uuid.NewString()
	var deviceID, operation string
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid model request")
		}
		searchDeclared := false
		tools, _ := body["tools"].([]any)
		for _, item := range tools {
			tool, _ := item.(map[string]any)
			function, _ := tool["function"].(map[string]any)
			if function["name"] != "skill_search" {
				continue
			}
			searchDeclared = true
			parameters, _ := function["parameters"].(map[string]any)
			if required, ok := parameters["required"].([]any); !ok || len(required) != 0 {
				t.Error("optional skill search arguments must encode an empty required array", parameters)
			}
			properties, _ := parameters["properties"].(map[string]any)
			if properties["query"] == nil {
				t.Error("skill search query missing from provider schema")
			}
		}
		if !searchDeclared {
			t.Error("enabled extension skill search missing from provider request")
		}
		switch calls.Add(1) {
		case 1:
			streamManagedTool(w, "skill_load", map[string]any{"binding_id": binding, "resource_id": "guide"})
		case 2:
			messages, _ := json.Marshal(body["messages"])
			if !strings.Contains(string(messages), "frozen instructions") {
				t.Error("skill snapshot missing")
			}
			streamManagedTool(w, "extension_exec", map[string]any{"binding_id": binding, "command": `printf '%s' "$GREETING" > "$JUEX_EXT_DATA_DIR/retained"; printf '%s' "$JUEX_EXT_DATA_DIR"`})
		case 4:
			streamManagedTool(w, "extension_observe", map[string]any{"binding_id": binding, "resource_id": "watch"})
		case 6:
			streamManagedTool(w, "subscribe", map[string]any{"kind": "command.observation", "environment_id": deviceID, "operation_id": operation})
		case 8:
			messages, _ := json.Marshal(body["messages"])
			if !strings.Contains(string(messages), "observed report") || !strings.Contains(string(messages), "artifact_id") {
				t.Error("observable artifact missing from event")
			}
			streamManagedReply(w, "Observation handled")
		default:
			streamManagedReply(w, "Ready")
		}
	})
	device, token := f.pairDevice(t)
	deviceID = device.ID
	directory := t.TempDir()
	manifest := extensionpolicy.Manifest{ManifestVersion: 2, Name: "working", Version: "1", Environment: map[string]string{"GREETING": "hello"}, Skills: []extensionpolicy.SkillResource{{ID: "guide", Path: "SKILL.md"}}, Observables: []extensionpolicy.ObservableResource{{CommandResource: extensionpolicy.CommandResource{ID: "watch", Command: []string{"/bin/sh", "-c", `printf x >> starts; while test ! -f release; do sleep 0.02; done; printf '%s\n' '{"message":"observed report","files":[{"path":"report.txt","name":"one.txt"},{"path":"report.txt","name":"two.txt"}]}'; sleep 60`}}, Options: execprotocol.ObservableOptions{Parser: execprotocol.ObservableParser{Type: "jsonl", ContentField: "message", AttachmentsField: "files"}, Batch: execprotocol.ObservableBatch{IntervalSeconds: 1}}}}}
	writeExtensionManifest(t, directory, manifest)
	for name, content := range map[string]string{"SKILL.md": "frozen instructions", "report.txt": "private attachment body"} {
		if err := os.WriteFile(filepath.Join(directory, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	objects, err := blob.Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = objects.Close() })
	f.execution.Blobs = &execution.ArtifactManager{Store: f.executionStore, Objects: objects, Capacity: 32 << 20}
	f.execution.Transfers = f.executionStore
	adapter, _ := extensionManagement(t, f)
	receipt := inspectManagedExtension(t, f, adapter, device.ID, directory)
	f.agent, err = adapter.Configure(ctx, f.actor, f.tenant, f.agent.ID, binding, management.ExtensionChange{Version: f.agent.Version, Enabled: true, EnvironmentID: device.ID, InspectionID: receipt.OperationID, Resources: []string{"skill/guide", "observable/watch"}})
	if err != nil {
		t.Fatal(err)
	}
	gateway := runtimeExecutionGateway(t, f)
	stop := runRuntimeTools(t, f, gateway)
	f.submit(t, "skill", f.main.ID, "Use the configured skill")
	hooksEventually(t, func() bool { return calls.Load() == 3 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	retained := filepath.Join(directory, ".juex-extensions", device.ID, f.agent.ID, binding, "retained")
	if data, err := os.ReadFile(retained); err != nil || string(data) != "hello" {
		t.Fatal("extension process environment", string(data), err)
	}
	f.submit(t, "observer", f.main.ID, "Start the observer")
	hooksEventually(t, func() bool { return calls.Load() == 5 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	if err := f.pool.QueryRow(ctx, `SELECT id FROM runtime.tools WHERE request->>'kind'='observe_command'`).Scan(&operation); err != nil {
		t.Fatal(err)
	}
	f.submit(t, "subscribe", f.main.ID, "Subscribe to its observations")
	hooksEventually(t, func() bool { return calls.Load() == 7 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	stop()
	if err := os.WriteFile(filepath.Join(directory, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	executionEventually(t, f, device.ID, operation, func(op execution.Operation) bool { return strings.Contains(op.Snapshot.Text(), "observed report") })
	runRuntimeTools(t, f, gateway)
	hooksEventually(t, func() bool {
		if err := f.execution.Reconcile(ctx); err != nil {
			t.Error(err)
		}
		return calls.Load() == 8 && f.timeline(t, f.main.ID).Thread.State == "idle"
	})
	hooksEventually(t, func() bool {
		var acked bool
		return f.pool.QueryRow(ctx, `SELECT confirmed_cursor=cursor AND cursor>0 FROM runtime.observation_sources WHERE operation_id=$1`, operation).Scan(&acked) == nil && acked
	})
	artifacts, err := f.execution.Artifacts(ctx, f.actor, f.tenant, f.agent.ID, "", 20)
	if err != nil || len(artifacts) != 2 {
		t.Fatal("same-path attachment identities collided", len(artifacts), err)
	}
	if data, err := os.ReadFile(filepath.Join(directory, "starts")); err != nil || string(data) != "x" {
		t.Fatal("observer restarted", string(data), err)
	}
	f.agent, err = adapter.Configure(ctx, f.actor, f.tenant, f.agent.ID, binding, management.ExtensionChange{Version: f.agent.Version, Enabled: false, Resources: []string{"skill/guide", "observable/watch"}})
	if err != nil {
		t.Fatal(err)
	}
	hooksEventually(t, func() bool {
		if err := f.execution.Reconcile(ctx); err != nil {
			t.Error(err)
		}
		op, err := f.executionStore.Operation(ctx, device.ID, operation, 0, 1)
		return err == nil && op.State == "cancelled"
	})
}
