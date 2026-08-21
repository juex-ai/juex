// Package builtintools adapts JueX's builtin Tool providers to the runtime
// Module framework.
package builtintools

import (
	"context"
	"fmt"
	"sync"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

const ModuleID runtimemodule.ID = "builtin-tools"

type Module struct {
	mu           sync.RWMutex
	baseContext  context.Context
	options      tools.BuiltinOptions
	ownedSession *tools.ShellSessionManager
	closeOnce    sync.Once
	closeErr     error
}

func New(ctx context.Context, options tools.BuiltinOptions) *Module {
	if ctx == nil {
		ctx = context.Background()
	}
	return &Module{baseContext: ctx, options: options}
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	if m == nil {
		return nil, nil
	}
	m.mu.RLock()
	options := m.options
	needsOwnedSession := m.ownedSession == nil && options.ShellSessions == nil
	m.mu.RUnlock()
	if needsOwnedSession {
		return nil, fmt.Errorf("builtin tools module has not started")
	}
	return tools.BuiltinTools(options), nil
}

func (m *Module) ShellSessions() *tools.ShellSessionManager {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.options.ShellSessions
}

func (m *Module) StartRuntime(context.Context, runtimemodule.RuntimeContext) error {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.options.ShellSessions == nil {
		m.ownedSession = tools.NewShellSessionManager(m.baseContext)
		m.options.ShellSessions = m.ownedSession
	}
	return nil
}

func (m *Module) CloseRuntime(context.Context) error {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	ownedSession := m.ownedSession
	m.mu.RUnlock()
	if ownedSession == nil {
		return nil
	}
	m.closeOnce.Do(func() {
		m.closeErr = ownedSession.Close()
	})
	return m.closeErr
}
