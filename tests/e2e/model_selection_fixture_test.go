//go:build postgres

package e2e

import (
	"context"

	"github.com/juex-ai/juex/internal/management"
	managementpg "github.com/juex-ai/juex/internal/management/postgres"
)

func (f *managedRuntimeFixture) configureModels(ctx context.Context, models []string) error {
	current, err := f.directory.ReadAgent(ctx, f.actor, f.tenant, f.agent.ID)
	if err != nil {
		return err
	}
	agent := current.Agent
	declaration := agent.Configuration.Clone()
	declaration.Models = models
	f.agent, err = f.directory.ConfigureAgent(ctx, f.actor, f.tenant, agent.ID, agent.Version, management.AgentConfig{Name: agent.Name, Instructions: agent.Instructions, WorkerDepth: agent.WorkerDepth, Hooks: agent.Hooks, DynamicInstructions: &agent.DynamicInstructions, Configuration: &declaration})
	return err
}

func configureTenantModels(ctx context.Context, d *managementpg.Directory, actor, tenant string, models []string) error {
	settings, err := d.TenantSettings(ctx, actor, tenant)
	if err != nil {
		return err
	}
	settings.Declaration.Models = models
	_, err = d.ConfigureTenantSettings(ctx, actor, tenant, settings)
	return err
}
