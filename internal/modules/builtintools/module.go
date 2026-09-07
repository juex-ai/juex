// Package builtintools adapts independent file capabilities to runtime Modules.
package builtintools

import (
	"context"

	"github.com/juex-ai/juex/internal/app/modulecatalog"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/tools"
)

type Module struct {
	id      runtimemodule.ID
	options tools.BuiltinOptions
}

func NewBasicFiles(options tools.BuiltinOptions) *Module {
	return newModule(modulecatalog.BasicFileTools, options, tools.FileToolProvider{})
}

func NewApplyPatch(options tools.BuiltinOptions) *Module {
	return newModule(modulecatalog.ApplyPatch, options, tools.PatchToolProvider{})
}

func NewFileSearch(options tools.BuiltinOptions) *Module {
	return newModule(modulecatalog.FileSearch, options, tools.SearchToolProvider{})
}

func newModule(id string, options tools.BuiltinOptions, provider tools.BuiltinProvider) *Module {
	options.Providers = []tools.BuiltinProvider{provider}
	return &Module{id: runtimemodule.ID(id), options: options}
}

func (m *Module) ID() runtimemodule.ID { return m.id }

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	return tools.BuiltinTools(m.options), nil
}
