package managedruntime

import (
	"context"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

func TestCapabilityRevocationSettlesPreparedOperationWithoutReplay(t *testing.T) {
	old := Scope{ActorID: "owner", TenantID: "tenant", AgentID: "agent", AgentExecutionEpoch: 1}
	fresh := old
	fresh.AgentExecutionEpoch++
	fresh.Capabilities.Disabled = []agentpolicy.Capability{agentpolicy.Shell}
	work := ToolWork{ID: "original", Scope: old, State: "waiting", EnvironmentID: "device", Request: execprotocol.Request{ID: "original"}, Call: llm.Block{ToolName: "exec_command"}, OperationLive: true}
	runner := toolRunner{authority: toolAuthority{scope: fresh}, gateway: cancellationToolGateway{state: execprotocol.Running}}
	outcome := runner.execute(context.Background(), &work)
	if !work.Cancelled || outcome.State != "waiting" || !outcome.OperationLive || outcome.RetryAfter <= 0 {
		t.Fatal("revoked request lost its cancellation receipt path", work, outcome)
	}
	// Re-enabling must not resubmit or keep an externally settled request live.
	fresh.Capabilities = agentpolicy.Policy{}
	runner.authority = toolAuthority{scope: fresh}
	runner.gateway = cancellationToolGateway{state: execprotocol.Cancelled}
	outcome = runner.execute(context.Background(), &work)
	if outcome.State != "cancelled" || outcome.OperationLive {
		t.Fatal("original cancellation could not settle", outcome)
	}
}

func TestDisabledObservationsAcknowledgePagesWithoutBufferedEvents(t *testing.T) {
	source := ObservationSource{Cursor: 2, Pending: []byte("partial"), Discarding: true, Command: CommandObservationBatch{Units: []CommandObservationUnit{{Content: "buffered command event"}}, Bytes: 22}}
	operation := ToolOperation{State: "completed", Snapshot: execprotocol.Snapshot{Output: []byte("page"), NextCursor: 6, OutputBytes: 10}}
	batch, err := acknowledgeOnlyObservation(source, operation)
	if err != nil || batch.Closed || !batch.More || batch.Cursor != 6 || len(batch.Pending) != 0 || batch.Discarding || len(batch.Command.Units) != 0 || len(batch.Facts) != 0 {
		t.Fatal("revoked consumer published buffers or closed before the final page", batch, err)
	}
	source.Cursor = batch.Cursor
	operation.Snapshot.NextCursor = 10
	batch, err = acknowledgeOnlyObservation(source, operation)
	if err != nil || !batch.Closed || batch.More || batch.Cursor != 10 || len(batch.Facts) != 0 {
		t.Fatal("final page was not confirmed without publishing", batch, err)
	}
	operation.Snapshot.NextCursor++
	if _, err := acknowledgeOnlyObservation(source, operation); err == nil {
		t.Fatal("invalid cursor was acknowledged")
	}
}

func TestDisabledCapabilitiesRejectFabricatedToolsBeforeDispatch(t *testing.T) {
	for _, test := range []struct {
		capability agentpolicy.Capability
		tool       string
	}{
		{agentpolicy.Memory, "memory_search"},
		{agentpolicy.Calendar, "calendar_change"},
		{agentpolicy.Workers, "thread_create"},
		{agentpolicy.Collaboration, "agent_send"},
		{agentpolicy.Files, "read"},
		{agentpolicy.Files, "publish_file"},
		{agentpolicy.Shell, "exec_command"},
		{agentpolicy.MCP, "mcp_connect"},
		{agentpolicy.Observations, "subscribe"},
		{agentpolicy.Extensions, "skill_load"},
		{agentpolicy.Extensions, "extension_exec"},
		{agentpolicy.Shell, "extension_exec"},
		{agentpolicy.MCP, "extension_mcp_connect"},
		{agentpolicy.Observations, "extension_observe"},
	} {
		for _, frozen := range []bool{false, true} {
			name := test.tool + "/current"
			if frozen {
				name = test.tool + "/frozen_then_enabled"
			}
			t.Run(name+"/"+string(test.capability), func(t *testing.T) {
				scope := Scope{ActorID: "owner", TenantID: "tenant", AgentID: "agent"}
				work := ToolWork{ID: "fabricated", Scope: scope, Call: llm.Block{ToolName: test.tool, ToolUseID: "fabricated"}}
				policy := agentpolicy.Policy{Disabled: []agentpolicy.Capability{test.capability}}
				if frozen {
					work.FrozenCapabilities = policy
				} else {
					scope.Capabilities = policy
				}
				runner := toolRunner{authority: toolAuthority{scope: scope}}
				result := runner.execute(context.Background(), &work)
				if !result.IsError || !strings.Contains(result.Content, "capability_disabled") || result.OperationLive {
					t.Fatalf("disabled tool was not rejected before dispatch: %+v", result)
				}
			})
		}
	}
}
