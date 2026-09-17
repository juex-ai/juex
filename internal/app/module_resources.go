package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/features/memory"
	"github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/module/state"
	"github.com/juex-ai/juex/internal/framework/thread"
)

// AcquireModuleResources commits an accepted configuration's resource lifecycle
// before publishing its endpoint. Close the lease after all Apps have stopped.
// Configuration previews and read-only handlers must not call this operation.
func AcquireModuleResources(cfg config.Config) (*state.Lease, error) {
	if err := ValidateModuleConfig(cfg); err != nil {
		return nil, err
	}
	return acquireModuleResources(cfg, nil)
}
func acquireModuleResources(cfg config.Config, extra []runtimemodule.ThreadFactorySpec) (*state.Lease, error) {
	specs := threadFactorySpecs(cfg, extra, nil, nil, cfg.WorkDir, threadModuleOptions{}, nil)
	owners := make([]state.Owner, 0, len(specs))
	seen := map[runtimemodule.ID]bool{}
	for _, spec := range specs {
		if !spec.Enabled {
			continue
		}
		if spec.New == nil || spec.ID == "" || seen[spec.ID] {
			return nil, fmt.Errorf("app: invalid Thread factory declaration %q", spec.ID)
		}
		seen[spec.ID] = true
		if spec.OwnsResources {
			owners = append(owners, state.Owner{Module: string(spec.ID), Scope: state.ScopeThread})
		}
	}
	dir := cfg.RuntimePaths().StateDir
	lease, err := state.Acquire(dir, owners, thread.NewStore(dir).ResourceDirectories)
	if err != nil {
		return nil, err
	}
	// Fix the owning Fleet identity even when this Agent starts before the
	// service. A later service launch can then satisfy its existing clients.
	if cfg.ModuleEnabled(memory.ModuleID) && cfg.HomeJuexDir != "" {
		if _, err := serviceendpoint.FleetID(cfg.HomeJuexDir); err != nil {
			_ = lease.Close()
			return nil, err
		}
	}
	if err := memory.ConfigureParticipation(dir, cfg.ModuleEnabled(memory.ModuleID)); err != nil {
		_ = lease.Close()
		return nil, err
	}
	if !cfg.ModuleEnabled(memory.ModuleID) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		err := memory.SyncDisabled(ctx, dir, func(id string) (memoryclient.API, memoryclient.Caller) {
			client, caller := memoryClient(cfg, id, nil)
			return client, caller
		})
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "juex: %v\n", err)
		}
	}
	return lease, nil
}
