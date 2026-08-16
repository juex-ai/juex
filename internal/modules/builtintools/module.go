// Package builtintools adapts JueX's builtin Tool providers to the runtime
// Module framework.
package builtintools

import (
	"context"
	"sync"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

const ModuleID runtimemodule.ID = "builtin-tools"

type Module struct {
	options      tools.BuiltinOptions
	ownedSession *tools.ShellSessionManager
	closeOnce    sync.Once
	closeErr     error
}

func New(ctx context.Context, options tools.BuiltinOptions) *Module {
	mod := &Module{options: options}
	if mod.options.ShellSessions == nil {
		mod.ownedSession = tools.NewShellSessionManager(ctx)
		mod.options.ShellSessions = mod.ownedSession
	}
	return mod
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	if m == nil {
		return nil, nil
	}
	return tools.BuiltinTools(m.options), nil
}

func (m *Module) ShellSessions() *tools.ShellSessionManager {
	if m == nil {
		return nil
	}
	return m.options.ShellSessions
}

func (*Module) StartRuntime(context.Context, runtimemodule.RuntimeContext) error {
	return nil
}

func (m *Module) CloseRuntime(context.Context) error {
	if m == nil || m.ownedSession == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.closeErr = m.ownedSession.Close()
	})
	return m.closeErr
}
