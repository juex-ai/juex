package agent

import (
	"github.com/juex-ai/juex/internal/foundation/tools"
	"github.com/juex-ai/juex/internal/framework/module"
)

// ModuleSnapshot is valid only inside ReadModuleSnapshot's callback.
type ModuleSnapshot struct {
	Tools          *tools.Registry
	Runtime        *module.Set
	Thread         *module.Set
	RuntimeContext module.RuntimeContext
	ThreadContext  module.ThreadContext
}
