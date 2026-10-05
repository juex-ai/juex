package managedruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type instructionGateway struct {
	ToolGateway
	environments []execprotocol.Environment
	operation    ToolOperation
	pages        map[int64]ToolOperation
	cancelled    string
	queries      []int64
}

func (g *instructionGateway) Environments(context.Context, Scope) ([]execprotocol.Environment, error) {
	return g.environments, nil
}
func (g *instructionGateway) Operation(_ context.Context, _ Scope, environment, id string, after int64) (ToolOperation, error) {
	g.queries = append(g.queries, after)
	if g.pages != nil {
		return g.pages[after], nil
	}
	return g.operation, nil
}

func TestInstructionReceiptCombinesPagesBeforePublishingSnapshot(t *testing.T) {
	ctx := context.Background()
	scope := Scope{ActorID: "owner", TenantID: "tenant", AgentID: "agent", AgentExecutionEpoch: 1}
	content := strings.Repeat("guidance ", 6000)
	source := func(path string) execprotocol.InstructionSource {
		return execprotocol.InstructionSource{Path: path, State: "loaded", Text: content, Size: int64(len(content)), SHA256: execprotocol.FileDigest([]byte(content))}
	}
	snapshot := execprotocol.InstructionSnapshot{Sources: []execprotocol.InstructionSource{source("/workspace/AGENTS.md"), source("/workspace/.agents/AGENTS.md")}}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	const split = 65536
	gateway := &instructionGateway{pages: map[int64]ToolOperation{
		0:     {State: "completed", Snapshot: execprotocol.Snapshot{Output: raw[:split], NextCursor: split, OutputBytes: int64(len(raw))}},
		split: {State: "completed", Snapshot: execprotocol.Snapshot{Output: raw[split:], NextCursor: int64(len(raw)), OutputBytes: int64(len(raw))}},
	}}
	runner := toolRunner{authority: toolAuthority{scope: scope}, gateway: gateway}
	work := InstructionWork{ID: "original", EnvironmentID: "original-device", Scope: scope, Request: execprotocol.Request{ID: "original", Kind: "read_agent_instructions", Arguments: json.RawMessage(`{"working_directory":"/workspace"}`)}}
	outcome := runner.executeInstructions(ctx, &work)
	if outcome.State != "ready" || outcome.Snapshot == nil || outcome.Snapshot.Sources[1].Text != content || outcome.OutputCursor != int64(len(raw)) || len(gateway.queries) != 2 || gateway.queries[1] != split {
		t.Fatal("paged receipt lost source bytes or acknowledgement cursor", outcome.State, outcome.OutputCursor, gateway.queries)
	}
}
func (g *instructionGateway) CancelPrepared(_ context.Context, _ Scope, environment, id string) (execprotocol.State, error) {
	g.cancelled = environment + ":" + id
	return execprotocol.Cancelled, nil
}

func TestPreparedInstructionsRecheckOriginalDeviceGrantBeforeModelDispatch(t *testing.T) {
	scope := Scope{ActorID: "owner", TenantID: "tenant", AgentID: "agent", AgentExecutionEpoch: 1}
	gateway := &instructionGateway{environments: defaultEnvironmentFixture(t)}
	runner := Runner{authority: toolAuthority{scope: scope}, tools: &toolRunner{gateway: gateway}}
	request := ModelRequest{DynamicInstructions: &InstructionReceipt{ID: "original", EnvironmentID: "mac", AuthorizationVersion: 7}}
	if err := runner.checkInstructionAuthority(context.Background(), Work{Scope: scope}, request); err != nil {
		t.Fatal(err)
	}
	// The selected default is descriptive; only the prepared device's original
	// grant can authorize its saved source bytes.
	gateway.environments[0].Default = true
	gateway.environments[1].Default = false
	if err := runner.checkInstructionAuthority(context.Background(), Work{Scope: scope}, request); err != nil {
		t.Fatal("default switch invalidated original grant", err)
	}
	gateway.environments[1].AuthorizationVersion++
	if err := runner.checkInstructionAuthority(context.Background(), Work{Scope: scope}, request); !errors.Is(err, ErrDenied) {
		t.Fatal("regranted device revived old source bytes", err)
	}
	gateway.environments = defaultEnvironmentFixture(t)
	gateway.environments[1].Capabilities = []execprotocol.Capability{execprotocol.Shell}
	if err := runner.checkInstructionAuthority(context.Background(), Work{Scope: scope}, request); !errors.Is(err, ErrDenied) {
		t.Fatal("Files revocation retained source authority", err)
	}
	gateway.environments = defaultEnvironmentFixture(t)
	fresh := scope
	fresh.AgentExecutionEpoch++
	runner.authority = toolAuthority{scope: fresh}
	if err := runner.checkInstructionAuthority(context.Background(), Work{Scope: scope}, request); !errors.Is(err, ErrDenied) {
		t.Fatal("re-enabled Agent revived old source bytes", err)
	}
}

func TestInstructionReceiptsRejectTruncationAndUnsupportedExecutor(t *testing.T) {
	scope := Scope{ActorID: "owner", TenantID: "tenant", AgentID: "agent", AgentExecutionEpoch: 1}
	request := execprotocol.Request{ID: "original", AgentID: scope.AgentID, Kind: "read_agent_instructions", Arguments: json.RawMessage(`{"working_directory":"/workspace"}`)}
	for _, tc := range []struct {
		name      string
		operation ToolOperation
		text      string
	}{
		{"expired", ToolOperation{State: "completed", Snapshot: execprotocol.Snapshot{OutputExpired: true}}, "incomplete"},
		{"truncated", ToolOperation{State: "completed", Snapshot: execprotocol.Snapshot{Truncated: true}}, "incomplete"},
		{"invalid-json", ToolOperation{State: "completed", Snapshot: execprotocol.Snapshot{Output: []byte("partial")}}, "does not match"},
		{"old-executor", ToolOperation{State: "failed", Snapshot: execprotocol.Snapshot{Error: "invalid"}}, "upgrade juex-executor"},
		{"unknown", ToolOperation{State: "unknown"}, "do not repeat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &instructionGateway{operation: tc.operation}
			runner := toolRunner{authority: toolAuthority{scope: scope}, gateway: gateway}
			work := InstructionWork{ID: request.ID, EnvironmentID: "original-device", Scope: scope, Request: request}
			outcome := runner.executeInstructions(context.Background(), &work)
			if outcome.State == "ready" || outcome.Snapshot != nil || !strings.Contains(outcome.Error, tc.text) || len(gateway.queries) != 1 {
				t.Fatal("unusable source became a model snapshot", outcome)
			}
		})
	}
}

func TestRevokedInstructionReadCancelsItsOriginalOperation(t *testing.T) {
	scope := Scope{ActorID: "owner", TenantID: "tenant", AgentID: "agent", AgentExecutionEpoch: 1}
	fresh := scope
	fresh.AgentExecutionEpoch++
	gateway := &instructionGateway{operation: ToolOperation{State: "cancelled", Snapshot: execprotocol.Snapshot{OutputBytes: 41}}}
	runner := toolRunner{authority: toolAuthority{scope: fresh}, gateway: gateway}
	work := InstructionWork{ID: "original", EnvironmentID: "original-device", Scope: scope, Request: execprotocol.Request{ID: "original"}}
	outcome := runner.executeInstructions(context.Background(), &work)
	if outcome.State != "cancelled" || outcome.OutputCursor != 41 || gateway.cancelled != "original-device:original" {
		t.Fatal("revocation lost original receipt", outcome, gateway.cancelled)
	}
}
