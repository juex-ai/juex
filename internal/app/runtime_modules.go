package app

import (
	"fmt"

	"github.com/juex-ai/juex/internal/config"
	memorymodule "github.com/juex-ai/juex/internal/memory/module"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

func newRuntimeModuleRegistry(cfg config.Config) (*runtimemodule.Registry, error) {
	registry := runtimemodule.NewRegistry()
	if err := registry.Register(memorymodule.New(cfg.MemoryDir()), cfg.MemoryModuleEnabled()); err != nil {
		return nil, fmt.Errorf("app: register runtime modules: %w", err)
	}
	return registry, nil
}
