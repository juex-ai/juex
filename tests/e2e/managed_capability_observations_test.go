//go:build postgres

package e2e

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/management"
)

func TestAgentObservationPolicyKeepsOutputAcknowledgmentWithoutPublication(t *testing.T) {
	for _, mode := range []string{"disabled", "frozen_disabled_then_enabled", "revoked_then_enabled"} {
		t.Run(mode, func(t *testing.T) {
			f := executionDatabase(t)
			ctx := context.Background()
			device, _ := f.pairDevice(t, execprotocol.MCP)
			configure := func(disabled bool) {
				t.Helper()
				policy := agentpolicy.Policy{}
				if disabled {
					policy.Disabled = []agentpolicy.Capability{agentpolicy.Observations}
				}
				var err error
				f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: f.agent.ModelID, Capabilities: &policy})
				if err != nil {
					t.Fatal(err)
				}
			}
			if mode != "revoked_then_enabled" {
				configure(true)
			}
			call := llm.Block{Type: llm.BlockToolUse, ToolUseID: "connect", ToolName: "mcp_connect", Input: map[string]any{"environment_id": device.ID, "command": "fixture"}}
			scope, work := prepareRuntimeCall(t, f.managedRuntimeFixture, f.main.ID, "prepare", call)
			if work.FrozenCapabilities.Allows(agentpolicy.Observations) != (mode == "revoked_then_enabled") {
				t.Fatal("ClaimTool lost the Turn's frozen policy", work.FrozenCapabilities)
			}
			request := execprotocol.Request{Version: execprotocol.Version, ID: work.ID, AgentID: f.agent.ID, Kind: "mcp_connect", Arguments: json.RawMessage(`{"command":"fixture"}`)}
			if err := f.store.PrepareTool(ctx, work, device.ID, request); err != nil {
				t.Fatal(err)
			}
			if mode == "frozen_disabled_then_enabled" {
				configure(false)
			}
			if _, err := f.execution.SubmitFenced(ctx, f.actor, f.tenant, device.ID, request, time.Hour, scope.ExecutionFence()); err != nil {
				t.Fatal(err)
			}
			connected, err := f.executionStore.Connect(ctx, device.ID, rand.Text())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.executionStore.Dispatch(ctx, device.ID, connected.ConnectionEpoch, work.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "revoked_then_enabled" {
				configure(true)
				configure(false)
			}
			// Multiple pages of valid notifications must be acknowledged but never
			// parsed into new Runtime events under frozen or revoked authority.
			output := []byte(strings.Repeat("{\"type\":\"notification\",\"method\":\"notifications/message\",\"params\":{\"text\":\"retained\"}}\n", 2000))
			snapshot := execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: device.ID, ID: work.ID, AgentID: f.agent.ID, Kind: request.Kind, State: execprotocol.Completed, Output: output, NextCursor: int64(len(output)), OutputBytes: int64(len(output))}
			if err := f.executionStore.Observe(ctx, device.ID, connected.ConnectionEpoch, snapshot); err != nil {
				t.Fatal(err)
			}
			if err := f.executionStore.Acknowledge(ctx, device.ID, connected.ConnectionEpoch, work.ID); err != nil {
				t.Fatal(err)
			}
			stop := runRuntimeTools(t, f, runtimeExecutionGateway(t, f))
			runtimeEventually(t, func() bool {
				var complete bool
				return f.pool.QueryRow(ctx, `SELECT closed AND cursor=$2 AND confirmed_cursor=cursor FROM runtime.observation_sources WHERE operation_id=$1`, work.ID, len(output)).Scan(&complete) == nil && complete
			})
			stop()
			var published int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.observations WHERE operation_id=$1`, work.ID).Scan(&published); err != nil || published != 0 {
				t.Fatal("disabled source published observations", published, err)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE execution.operations SET updated_at=clock_timestamp()-interval '8 days',observed_at=clock_timestamp()-interval '8 days' WHERE id=$1`, work.ID); err != nil {
				t.Fatal(err)
			}
			if err := f.executionStore.ExpireWaiting(ctx); err != nil {
				t.Fatal(err)
			}
			operation, err := f.executionStore.Operation(ctx, device.ID, work.ID, int64(len(output)), 1)
			if err != nil || !operation.Snapshot.OutputExpired {
				t.Fatal("revoked consumer leaked retained output", operation.Snapshot.OutputExpired, err)
			}
		})
	}
}
