package app

import (
	"github.com/juex-ai/juex/internal/app/config"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

// ThreadInspectionCatalog uses the same effective declarations as runtime
// construction, without invoking factories or acquiring module resources.
func ThreadInspectionCatalog(cfg config.Config, extra ...runtimemodule.ThreadFactorySpec) (*runtimemodule.InspectionCatalog, error) {
	return runtimemodule.NewInspectionCatalog(threadFactorySpecs(cfg, extra, nil, nil, cfg.WorkDir, threadModuleOptions{}, nil))
}
