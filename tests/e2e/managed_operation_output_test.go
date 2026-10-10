//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/entrypoints/managementhttp"
	"github.com/juex-ai/juex/internal/execution/native"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedOperationOutputStreamsOriginalDeviceAndScopesReceipt(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	device, token := f.pairDevice(t)
	work := t.TempDir()
	engine := openNative(t, native.Config{StateDirectory: filepath.Join(t.TempDir(), "state"), EnvironmentID: device.ID, WorkingDirectory: work, Grants: device.Ceiling})
	connectExecutionDevice(t, f, device, token, engine)
	_, origin := extensionManagement(t, f)
	arguments, _ := json.Marshal(map[string]any{"command": "printf 'first\\n'; while test ! -f release; do sleep 0.02; done; printf 'second 中文\\n'", "working_directory": work})
	op, err := f.execution.Submit(ctx, f.actor, f.tenant, device.ID, execprotocol.Request{Version: execprotocol.Version, ID: uuid.NewString(), AgentID: f.agent.ID, Kind: "exec_command", Arguments: arguments, AuthorizationVersion: device.Version}, 0)
	if err != nil {
		t.Fatal(err)
	}
	base := origin + "/api/tenants/" + f.tenant + "/agents/" + f.agent.ID + "/environments/" + device.ID + "/operations/" + op.ID + "/output"
	var first managementhttp.OperationOutput
	hooksEventually(t, func() bool {
		first = managementCall[managementhttp.OperationOutput](t, f.client, "GET", base, origin, nil, 200)
		return string(first.Output) == "first\n"
	})
	if first.State != "running" || first.NextCursor != 6 {
		t.Fatal("did not see output before completion", first)
	}
	if err := os.WriteFile(filepath.Join(work, "release"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	var last managementhttp.OperationOutput
	hooksEventually(t, func() bool {
		last = managementCall[managementhttp.OperationOutput](t, f.client, "GET", fmt.Sprintf("%s?after=%d", base, first.NextCursor), origin, nil, 200)
		return last.State == "completed" && string(last.Output) == "second 中文\n"
	})
	if last.NextCursor != int64(len("first\nsecond 中文\n")) || last.NextCursor != last.OutputBytes || last.ExitCode == nil || *last.ExitCode != 0 {
		t.Fatal("incorrect retained output", last)
	}
	raw := managementCall[map[string]any](t, f.client, "GET", base, origin, nil, 200)
	for _, key := range []string{"request", "scope", "arguments", "environment"} {
		if _, ok := raw[key]; ok {
			t.Fatal("private operation fields exposed", key)
		}
	}
	for _, after := range []string{"bad", "-1", "999999"} {
		managementCall[any](t, f.client, "GET", base+"?after="+after, origin, nil, 400)
	}
	other, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Other"})
	if err != nil {
		t.Fatal(err)
	}
	managementCall[any](t, f.client, "GET", origin+"/api/tenants/"+f.tenant+"/agents/"+other.ID+"/environments/"+device.ID+"/operations/"+op.ID+"/output", origin, nil, 403)
}
