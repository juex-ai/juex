//go:build postgres

package e2e

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedWorkspaceConfigurationUsesRetainedDeviceSnapshotAndFreshAuthority(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, token := f.pairDevice(t)
	directory := t.TempDir()
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	_, origin := extensionManagement(t, f)
	base := origin + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID
	primary := f.agent.Configuration.Models[0]
	tenantSettings, err := f.directory.TenantSettings(ctx, f.actor, f.tenant)
	if err != nil {
		t.Fatal(err)
	}
	tenantSettings.Declaration = management.Configuration{Models: []string{primary}, Modules: map[agentpolicy.Capability]bool{agentpolicy.MCP: false}}
	if _, err := f.directory.ConfigureTenantSettings(ctx, f.actor, f.tenant, tenantSettings); err != nil {
		t.Fatal(err)
	}
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, Configuration: &management.Configuration{}})
	if err != nil {
		t.Fatal(err)
	}
	old, err := f.directory.AuthorizeAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	read := func(content string, read bool) management.WorkspaceReadReceipt {
		t.Helper()
		if err := os.WriteFile(filepath.Join(directory, "juex.yaml"), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		query := execprotocol.WorkspaceQuery{Path: "juex.yaml", Read: true}
		if !read {
			query = execprotocol.WorkspaceQuery{Path: "."}
		}
		request := management.WorkspaceReadRequest{RequestID: uuid.NewString(), EnvironmentID: device.ID, Directory: directory, Query: query}
		receipt := managementCall[management.WorkspaceReadReceipt](t, f.client, "POST", base+"/workspace-reads", origin, request, 200)
		hooksEventually(t, func() bool {
			receipt = managementCall[management.WorkspaceReadReceipt](t, f.client, "GET", base+"/workspace-reads/"+device.ID+"/"+receipt.OperationID, origin, nil, 200)
			return receipt.State == "completed"
		})
		return receipt
	}
	content := "models: [fixture:test-model]\nmodules: {memory: false, mcp: true}\n"
	receipt := read(content, true)
	previewURL := base + "/workspace-configurations/" + device.ID + "/" + receipt.OperationID
	preview := managementCall[management.WorkspaceConfigurationPreview](t, f.client, "GET", previewURL, origin, nil, 200)
	if preview.Configuration == nil || preview.Receipt.Listing != nil || preview.Configuration.WorkingDirectory != directory || preview.Configuration.Path != "juex.yaml" || preview.Configuration.AuthorizationVersion != device.Version || preview.Effective.ModelSource.Layer != "workspace" || !slices.Equal(preview.Configuration.Declaration.Models, []string{primary}) {
		t.Fatal("incorrect typed preview", preview)
	}
	before := managementCall[management.AgentDetail](t, f.client, "GET", base, origin, nil, 200)
	if before.Agent.WorkspaceConfiguration != nil {
		t.Fatal("preview applied configuration")
	}
	// Applying is tied to the already inspected bytes even if the source changes.
	if err := os.WriteFile(filepath.Join(directory, "juex.yaml"), []byte("modules: {memory: true, mcp: false}"), 0600); err != nil {
		t.Fatal(err)
	}
	change := management.WorkspaceConfigurationChange{Version: f.agent.Version, EnvironmentID: device.ID, OperationID: receipt.OperationID, SHA256: preview.Configuration.SHA256}
	bad := change
	bad.SHA256 = strings.Repeat("0", 64)
	managementCall[any](t, f.client, "PUT", base+"/workspace-configuration", origin, bad, 409)
	f.agent = managementCall[management.Agent](t, f.client, "PUT", base+"/workspace-configuration", origin, change, 200)
	effective := managementCall[management.AgentDetail](t, f.client, "GET", base, origin, nil, 200)
	appliedAuthority, err := f.directory.AuthorizeAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if effective.Effective.Policy().Allows(agentpolicy.Memory) || !effective.Effective.Policy().Allows(agentpolicy.MCP) || appliedAuthority.Agent.ExecutionEpoch != old.Agent.ExecutionEpoch+1 || f.agent.WorkspaceConfiguration.OperationID != receipt.OperationID {
		t.Fatal("snapshot did not control fresh authority", effective)
	}
	if _, err := f.directory.SnapshotPlan(ctx, modelCallScope(old)); !errors.Is(err, management.ErrDenied) {
		t.Fatal("workspace restriction failed to fence old scope", err)
	}
	managementCall[any](t, f.client, "PUT", base+"/workspace-configuration", origin, change, 409)
	removed := managementCall[management.Agent](t, f.client, "PUT", base+"/workspace-configuration", origin, management.WorkspaceConfigurationChange{Version: f.agent.Version, Remove: true}, 200)
	effective = managementCall[management.AgentDetail](t, f.client, "GET", base, origin, nil, 200)
	removedAuthority, err := f.directory.AuthorizeAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if removed.WorkspaceConfiguration != nil || removedAuthority.Agent.ExecutionEpoch != appliedAuthority.Agent.ExecutionEpoch+1 || effective.Effective.ModelSource.Layer != "tenant" || !effective.Effective.Policy().Allows(agentpolicy.Memory) || effective.Effective.Policy().Allows(agentpolicy.MCP) {
		t.Fatal("removing snapshot did not restore and fence lower configuration", effective)
	}
	f.agent = removed
	for _, item := range []struct {
		content string
		read    bool
	}{{"providers: {private: {api_key: secret}}", true}, {strings.Repeat("# long\n", 50000), true}, {"{}", false}} {
		rejected := read(item.content, item.read)
		managementCall[any](t, f.client, "GET", base+"/workspace-configurations/"+device.ID+"/"+rejected.OperationID, origin, nil, 400)
	}
	if _, err := f.execution.Restrict(ctx, f.actor, f.tenant, device.ID, device.Version, map[string][]execprotocol.Capability{f.agent.ID: {execprotocol.Files}}); err != nil {
		t.Fatal(err)
	}
	change.Version = f.agent.Version
	managementCall[any](t, f.client, "PUT", base+"/workspace-configuration", origin, change, 403)
}
