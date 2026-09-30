package managed

import (
	"context"

	"github.com/juex-ai/juex/internal/execution"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/managedruntime"
)

type RuntimeTools struct{ Client *executionrpc.Client }

func (g RuntimeTools) Environments(ctx context.Context, scope managedruntime.Scope) ([]execprotocol.Environment, error) {
	return g.Client.Environments(ctx, scope.ActorID, scope.TenantID, scope.AgentID)
}
func toolOperation(op execution.Operation) managedruntime.ToolOperation {
	return managedruntime.ToolOperation{State: op.State, CancelRequested: op.CancelRequested, WaitUntil: op.WaitUntil, Snapshot: op.Snapshot}
}
func (g RuntimeTools) Submit(ctx context.Context, scope managedruntime.Scope, environment string, request execprotocol.Request) (managedruntime.ToolOperation, error) {
	op, err := g.Client.SubmitFenced(ctx, scope.ActorID, scope.TenantID, environment, request, 0, scope.ExecutionFence())
	return toolOperation(op), err
}
func (g RuntimeTools) Operation(ctx context.Context, scope managedruntime.Scope, environment, id string, after int64) (managedruntime.ToolOperation, error) {
	op, err := g.Client.Operation(ctx, scope.ActorID, scope.TenantID, scope.AgentID, environment, id, after, 64<<10)
	return toolOperation(op), err
}
func (g RuntimeTools) Cancel(ctx context.Context, scope managedruntime.Scope, environment, id string) error {
	return g.Client.Cancel(ctx, scope.ActorID, scope.TenantID, scope.AgentID, environment, id)
}
func (g RuntimeTools) Events(ctx context.Context, limit int) ([]execprotocol.Event, error) {
	return g.Client.Events(ctx, limit)
}
func (g RuntimeTools) AcknowledgeEvents(ctx context.Context, ids []string) error {
	return g.Client.AcknowledgeEvents(ctx, ids)
}
