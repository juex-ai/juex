//go:build postgres

package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/native"
	executionpg "github.com/juex-ai/juex/internal/execution/postgres"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type maintenancePurgeBackend struct {
	hostedBackendProbe
	purges int
}

func (b *maintenancePurgeBackend) Purge(context.Context, execution.HostedResource) error {
	b.purges++
	return nil
}

func TestExecutionMaintenanceRequiresConfirmedHostedDestruction(t *testing.T) {
	for _, kind := range []string{"hosted", "native"} {
		for _, state := range []execprotocol.State{execprotocol.Running, execprotocol.Unknown} {
			t.Run(kind+"/"+string(state), func(t *testing.T) {
				f := managedPurge(t)
				ctx := context.Background()
				backend := &maintenancePurgeBackend{}
				var environment string
				if kind == "hosted" {
					f.execution.Hosted = &execution.HostedManager{Store: f.executionStore, Backend: backend, Authority: f.execution.Authority, Key: make([]byte, 32), Idle: time.Minute, StorageIdentity: uuid.NewString()}
					envs, err := f.execution.Environments(ctx, f.actor, f.tenant, f.agent.ID)
					if err != nil || len(envs) != 1 {
						t.Fatal(envs, err)
					}
					environment = envs[0].ID
				} else {
					device, _ := f.pairDevice(t)
					environment = device.ID
				}
				connected, err := f.executionStore.Connect(ctx, environment, uuid.NewString())
				if err != nil {
					t.Fatal(err)
				}
				request := nativeRequest(t, "purged-operation", "exec_command", native.CommandArguments{Command: "sleep 3600"})
				request.AgentID = f.agent.ID
				if _, err := f.execution.Submit(ctx, f.actor, f.tenant, environment, request, 0); err != nil {
					t.Fatal(err)
				}
				if _, err := f.executionStore.Dispatch(ctx, environment, connected.ConnectionEpoch, request.ID); err != nil {
					t.Fatal(err)
				}
				if err := f.executionStore.Observe(ctx, environment, connected.ConnectionEpoch, execprotocol.Snapshot{Version: 1, EnvironmentID: environment, ID: request.ID, AgentID: f.agent.ID, Kind: request.Kind, State: state}); err != nil {
					t.Fatal(err)
				}
				before, err := executionpg.MaintenanceReport(ctx, f.pool)
				if err != nil || before.Ready() || len(before.Busy) != 1 {
					t.Fatal("unconfirmed process must prevent backup", before, err)
				}
				job := f.archiveAndPurge(t)
				report, err := executionpg.MaintenanceReport(ctx, f.pool)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "native" {
					if report.Ready() || len(report.Busy) != 1 || job.Receipts["execution"].Unconfirmed != 1 {
						t.Fatal("native cleanup incorrectly confirmed process shutdown", report, job)
					}
					return
				}
				if backend.purges != 1 || job.Receipts["execution"].Unconfirmed != 0 || !job.Receipts["execution"].DataRemoved {
					t.Fatal("hosted destruction was not confirmed", backend.purges, job)
				}
				if !report.Ready() {
					t.Fatal("destroyed hosted environment still blocks backup", report.Busy)
				}
				var retained bool
				for _, item := range report.Review {
					if item.Kind == "destroyed_hosted_operation" && strings.HasSuffix(item.ID, "/"+request.ID) && item.State == string(state) {
						retained = true
					}
				}
				if !retained {
					t.Fatal("backup review lost the original operation outcome", report.Review)
				}
			})
		}
	}
}
