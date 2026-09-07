package app

import (
	"fmt"

	"github.com/juex-ai/juex/internal/framework/agent"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func (a *App) ReadModuleSnapshot(fn func(agent.ModuleSnapshot) error) error {
	if a == nil || fn == nil {
		return fmt.Errorf("runtime status: active App and snapshot reader are required")
	}
	a.lifecycleMu.RLock()
	defer a.lifecycleMu.RUnlock()
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	if a.Engine == nil || a.runtimeModules == nil {
		return fmt.Errorf("runtime status: active Runtime Module set is unavailable")
	}
	threadRuntime := a.Engine.ThreadRuntimeSnapshot()
	if threadRuntime.Modules == nil || threadRuntime.Thread == nil {
		return fmt.Errorf("runtime status: active Thread Module set is unavailable")
	}
	return fn(agent.ModuleSnapshot{
		Tools:          threadRuntime.Tools,
		Runtime:        a.runtimeModules,
		Thread:         threadRuntime.Modules,
		RuntimeContext: a.runtimeModuleContext,
		ThreadContext:  runtimemodule.ThreadContext{ID: threadRuntime.Thread.ID, Dir: threadRuntime.Thread.Dir},
	})
}
