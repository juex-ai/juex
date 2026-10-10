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

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedPatchRuntimeRPCAndRevocation(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: source.txt\n-old\n+新内容\n*** Add File: nested/new.txt\n+created\n*** End Patch"
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if calls.Add(1) == 1 {
			tools, _ := json.Marshal(body["tools"])
			if !strings.Contains(string(tools), `"name":"apply_patch"`) {
				t.Error("enabled tool missing from frozen catalog")
			}
			streamManagedTool(w, "apply_patch", map[string]any{"patch_text": patch})
			return
		}
		messages, _ := json.Marshal(body["messages"])
		if !strings.Contains(string(messages), "applied patch") {
			t.Error("patch receipt missing from model history")
		}
		streamManagedReply(w, "Patch done")
	})
	ctx := context.Background()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "source.txt"), []byte("old\n"), 0750); err != nil {
		t.Fatal(err)
	}
	device, token := f.pairDevice(t)
	_, err := f.execution.SetDefaultEnvironment(ctx, f.actor, f.tenant, f.agent.ID, execution.DefaultEnvironment{EnvironmentID: device.ID, WorkingDirectory: work})
	if err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: t.TempDir(), Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	args, _ := json.Marshal(map[string]any{"working_directory": work, "patch_text": patch})
	request := execprotocol.Request{Version: execprotocol.Version, ID: uuid.NewString(), AgentID: f.agent.ID, Kind: "apply_patch", Arguments: args, AuthorizationVersion: device.Version}
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("default-off direct operation accepted", err)
	}
	configure := func(enabled bool) {
		t.Helper()
		view, err := f.directory.ReadAgent(ctx, f.actor, f.tenant, f.agent.ID)
		if err != nil {
			t.Fatal(err)
		}
		configuration := view.Agent.Configuration.Clone()
		if configuration.Modules == nil {
			configuration.Modules = map[agentpolicy.Capability]bool{}
		}
		configuration.Modules[agentpolicy.ApplyPatch] = enabled
		f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, view.Agent.Version, management.AgentConfig{Name: view.Agent.Name, Instructions: view.Agent.Instructions, Configuration: &configuration})
		if err != nil {
			t.Fatal(err)
		}
	}
	configure(true)
	gateway := runtimeExecutionGateway(t, f)
	runRuntimeTools(t, f, gateway)
	f.submit(t, "patch-input", f.main.ID, "Apply the supplied patch")
	runtimeEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	for path, want := range map[string]string{"source.txt": "新内容\n", "nested/new.txt": "created\n"} {
		data, err := os.ReadFile(filepath.Join(work, path))
		if err != nil || string(data) != want {
			t.Fatal(path, string(data), err)
		}
	}
	info, err := os.Stat(filepath.Join(work, "source.txt"))
	if err != nil || info.Mode().Perm() != 0750 {
		t.Fatal(info, err)
	}
	assertRuntimeTranscript(t, f)
	before := f.agent.ExecutionEpoch
	configure(false)
	if f.agent.ExecutionEpoch != before+1 {
		t.Fatal("optional module removal did not revoke epoch")
	}
	if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); !errors.Is(err, execprotocol.ErrDenied) {
		t.Fatal("revoked direct patch accepted", err)
	}
	configure(true)
	if f.agent.ExecutionEpoch != before+1 {
		t.Fatal("re-enable revived old authority")
	}
}
