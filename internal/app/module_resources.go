package app

import (
	"fmt"

	"github.com/juex-ai/juex/internal/config"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/runtime/module/state"
	"github.com/juex-ai/juex/internal/thread"
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
	return state.Acquire(dir, owners, thread.NewStore(dir).ResourceDirectories)
}
