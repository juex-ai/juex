//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedSkillDirectorySnapshotIndependentAuthority(t *testing.T) {
	ctx := context.Background()
	binding := uuid.NewString()
	var calls atomic.Int32
	f := executionDatabaseWithProvider(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("bad provider request")
		}
		tools, _ := json.Marshal(body["tools"])
		if strings.Contains(string(tools), `"name":"read"`) || strings.Contains(string(tools), `"name":"extension_exec"`) || !strings.Contains(string(tools), `"name":"skill_load"`) {
			t.Error("skills-only catalog expanded or disappeared", string(tools))
		}
		if calls.Add(1) == 1 {
			streamManagedTool(w, "skill_load", map[string]any{"binding_id": binding, "resource_id": "skill00"})
			return
		}
		messages, _ := json.Marshal(body["messages"])
		if !strings.Contains(string(messages), "FROZEN-SKILLS-ROOT") || strings.Contains(string(messages), "DISK-CHANGED") {
			t.Error("saved skill was not replayed")
		}
		if !strings.Contains(string(messages), "skill00/SKILL.md") {
			t.Error("loaded skill lost entrypoint")
		}
		streamManagedReply(w, "Loaded frozen source")
	})
	device, token := f.pairDevice(t)
	dir := t.TempDir()
	for i := 0; i < 48; i++ {
		name := fmt.Sprintf("skill%02d", i)
		if err := os.Mkdir(filepath.Join(dir, name), 0755); err != nil {
			t.Fatal(err)
		}
		body := "---\nname: " + name + "\ndescription: User root guidance\n---\nFROZEN-SKILLS-ROOT\n" + strings.Repeat("bounded guidance\n", 600)
		if err := os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	connectExecutionDevice(t, f, device, token, openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: dir, Grants: device.Ceiling}))
	adapter, origin := extensionManagement(t, f)
	base := origin + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID
	configure := func(modules map[agentpolicy.Capability]bool) {
		t.Helper()
		var err error
		f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, Configuration: &management.Configuration{Models: f.agent.Configuration.Models, Modules: modules}, WorkerDepth: 1})
		if err != nil {
			t.Fatal(err)
		}
	}
	configure(map[agentpolicy.Capability]bool{agentpolicy.Files: false})
	request := management.ExtensionInspectionRequest{RequestID: uuid.NewString(), EnvironmentID: device.ID, Directory: dir, SourceKind: "skills"}
	managementCall[any](t, f.client, "POST", base+"/extension-inspections", origin, request, 403)
	configure(map[agentpolicy.Capability]bool{agentpolicy.Files: false, agentpolicy.Extensions: false, agentpolicy.Skills: true, agentpolicy.FileSearch: true})
	search := nativeRequest(t, "independent-search", "glob", map[string]any{"pattern": "**/SKILL.md", "path": dir})
	search.AgentID = f.agent.ID
	operation, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, search, 0)
	if err != nil {
		t.Fatal("search-only request denied", err)
	}
	hooksEventually(t, func() bool {
		operation, err = f.execution.Operation(ctx, f.actor, f.tenant, f.agent.ID, device.ID, search.ID, 0, 64<<10)
		return err == nil && operation.State == "completed"
	})
	if !strings.Contains(string(operation.Snapshot.Output), "skill00/SKILL.md") {
		t.Fatal("search output missing", string(operation.Snapshot.Output))
	}
	for _, kind := range []string{"read", "write"} {
		request := nativeRequest(t, "denied-"+kind, kind, map[string]any{"path": filepath.Join(dir, "skill00", "SKILL.md"), "content": "must not write"})
		request.AgentID = f.agent.ID
		if _, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, request, 0); err == nil {
			t.Fatal("search granted file access", kind)
		}
	}
	request.RequestID = uuid.NewString()
	receipt := managementCall[management.ExtensionInspection](t, f.client, "POST", base+"/extension-inspections", origin, request, 200)
	hooksEventually(t, func() bool {
		var err error
		receipt, err = adapter.Inspection(ctx, f.actor, f.tenant, f.agent.ID, device.ID, receipt.OperationID)
		return err == nil && receipt.State == "completed"
	})
	if receipt.SourceKind != "skills" || receipt.Catalog == nil || len(receipt.Catalog.Skills) != 48 {
		t.Fatal("real sized root not captured", receipt.State)
	}
	encoded, _ := json.Marshal(receipt.Catalog)
	if len(encoded) <= 256<<10 {
		t.Fatal("fixture does not cover previous catalog bound")
	}
	change := management.ExtensionChange{Version: f.agent.Version, Enabled: true, InspectionID: receipt.OperationID, EnvironmentID: device.ID, Resources: []string{"skill/skill00"}}
	f.agent = managementCall[management.Agent](t, f.client, "PUT", base+"/extensions/"+binding, origin, change, 200)
	if err := os.WriteFile(filepath.Join(dir, "skill00", "SKILL.md"), []byte("DISK-CHANGED"), 0600); err != nil {
		t.Fatal(err)
	}
	stop := runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
	defer stop()
	f.submit(t, "load-independent-skill", f.main.ID, "Load the selected skill")
	hooksEventually(t, func() bool { return calls.Load() == 2 && f.timeline(t, f.main.ID).Thread.State == "idle" })
	oldEpoch := f.agent.ExecutionEpoch
	configure(map[agentpolicy.Capability]bool{agentpolicy.Files: false, agentpolicy.Extensions: false, agentpolicy.Skills: false})
	configure(map[agentpolicy.Capability]bool{agentpolicy.Files: false, agentpolicy.Extensions: false, agentpolicy.Skills: true})
	if f.agent.ExecutionEpoch <= oldEpoch {
		t.Fatal("revocation did not fence authority")
	}
	change.Version = f.agent.Version
	managementCall[any](t, f.client, "PUT", base+"/extensions/"+binding, origin, change, 403)
	change.InspectionID = ""
	managementCall[any](t, f.client, "PUT", base+"/extensions/"+binding, origin, change, 403)
}
