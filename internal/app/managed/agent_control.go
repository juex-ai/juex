package managed

import (
	"context"
	"github.com/juex-ai/juex/internal/foundation/agentcontrol"
)

func (a RuntimeAuthority) ControlAgents(ctx context.Context, source agentcontrol.Source, id, after string) (agentcontrol.Page, error) {
	value, err := a.Directory.ControlAgents(ctx, source, id, after)
	return value, runtimeError(err)
}
func (a RuntimeAuthority) ControlAgent(ctx context.Context, source agentcontrol.Source, action agentcontrol.Action, cancel bool) (agentcontrol.Receipt, error) {
	value, err := a.Directory.ControlAgent(ctx, source, action, cancel)
	return value, runtimeError(err)
}
