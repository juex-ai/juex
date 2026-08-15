package app

import runtimemodule "github.com/juex-ai/juex/internal/runtime/module"

func newRuntimeModuleRegistry() *runtimemodule.Registry {
	return runtimemodule.NewRegistry()
}
