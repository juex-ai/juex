package applypatch

import (
	"context"

	"github.com/juex-ai/juex/internal/foundation/tools"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

const ModuleID runtimemodule.ID = "apply-patch"

type Module struct{ options Options }

func New(options Options) *Module    { return &Module{options: options} }
func (*Module) ID() runtimemodule.ID { return ModuleID }
func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	return Contributions(m.options), nil
}
