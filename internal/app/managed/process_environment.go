package managed

import (
	"context"
	"slices"

	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

type ManagementProcessEnvironment struct {
	Directory *managementpg.Directory
	Execution *executionrpc.Client
}

func (s ManagementProcessEnvironment) Read(ctx context.Context, actor, tenant, agent string) (management.ProcessEnvironmentView, error) {
	return s.Directory.ProcessEnvironment(ctx, actor, tenant, agent)
}
func (s ManagementProcessEnvironment) Configure(ctx context.Context, actor, tenant, agent, layer string, change management.ProcessEnvironmentChange) (management.ProcessEnvironmentView, error) {
	if layer == "workspace" && change.EnvironmentID != "" && len(change.Set) != 0 {
		inspection, err := s.Execution.InspectEnvironments(ctx, actor, tenant, agent)
		if err != nil {
			return management.ProcessEnvironmentView{}, err
		}
		allowed := false
		for _, e := range inspection.Environments {
			allowed = allowed || e.ID == change.EnvironmentID && (slices.Contains(e.Capabilities, execprotocol.Shell) || slices.Contains(e.Capabilities, execprotocol.MCP))
		}
		if !allowed {
			return management.ProcessEnvironmentView{}, management.ErrDenied
		}
	}
	return s.Directory.ConfigureProcessEnvironment(ctx, actor, tenant, agent, layer, change)
}
