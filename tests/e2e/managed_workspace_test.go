//go:build postgres

package e2e

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedWorkspaceReadsSelectedDeviceAndRetainsReceipt(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, token := f.pairDevice(t)
	directory := t.TempDir()
	content := strings.Repeat("selected device 中文\n", 6000)
	if err := os.WriteFile(filepath.Join(directory, "readme.txt"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: directory, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	_, origin := extensionManagement(t, f)
	base := origin + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID
	request := management.WorkspaceReadRequest{RequestID: uuid.NewString(), EnvironmentID: device.ID, Directory: directory, Query: execprotocol.WorkspaceQuery{Path: "readme.txt", Read: true}}
	receipt := managementCall[management.WorkspaceReadReceipt](t, f.client, "POST", base+"/workspace-reads", origin, request, 200)
	endpoint := base + "/workspace-reads/" + device.ID + "/" + receipt.OperationID
	hooksEventually(t, func() bool {
		receipt = managementCall[management.WorkspaceReadReceipt](t, f.client, "GET", endpoint, origin, nil, 200)
		return receipt.State == "completed"
	})
	if receipt.EnvironmentID != device.ID || receipt.Directory != directory || receipt.Listing == nil || receipt.Listing.Preview == nil || receipt.Listing.Preview.Text != content {
		t.Fatal("device preview or chunked output differs", receipt)
	}
	if err := os.WriteFile(filepath.Join(directory, "readme.txt"), []byte("later file"), 0600); err != nil {
		t.Fatal(err)
	}
	frozen := managementCall[management.WorkspaceReadReceipt](t, f.client, "POST", base+"/workspace-reads", origin, request, 200)
	if frozen.OperationID != receipt.OperationID || frozen.Listing == nil || frozen.Listing.Preview.Text != content {
		t.Fatal("retry read mutable file instead of receipt")
	}
	request.RequestID = uuid.NewString()
	latest := managementCall[management.WorkspaceReadReceipt](t, f.client, "POST", base+"/workspace-reads", origin, request, 200)
	hooksEventually(t, func() bool {
		latest = managementCall[management.WorkspaceReadReceipt](t, f.client, "GET", base+"/workspace-reads/"+device.ID+"/"+latest.OperationID, origin, nil, 200)
		return latest.State == "completed"
	})
	if latest.Listing == nil || latest.Listing.Preview.Text != "later file" {
		t.Fatal("refresh did not issue a new read")
	}
	request.RequestID = uuid.NewString()
	request.Query.Path = "../escape"
	managementCall[any](t, f.client, "POST", base+"/workspace-reads", origin, request, 400)
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	managementCall[any](t, f.client, "GET", origin+"/api/tenants/"+f.tenant+"/agents/"+other.ID+"/workspace-reads/"+device.ID+"/"+receipt.OperationID, origin, nil, 403)
	if _, err := f.execution.Restrict(ctx, f.actor, f.tenant, device.ID, device.Version, map[string][]execprotocol.Capability{f.agent.ID: {execprotocol.Shell}}); err != nil {
		t.Fatal(err)
	}
	request.RequestID = uuid.NewString()
	request.Query.Path = "readme.txt"
	managementCall[any](t, f.client, "POST", base+"/workspace-reads", origin, request, 403)
	managementCall[any](t, f.client, "GET", endpoint, origin, nil, 403)
}
