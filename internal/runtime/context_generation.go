package runtime

import (
	"context"
	"fmt"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
)

// NewContext starts an empty Context Generation on the current Thread. It is
// serialized with Turns and clears Thread work state while preserving the
// Thread journal and scratchpad.
func (e *Engine) NewContext(ctx context.Context) error {
	if e == nil {
		return fmt.Errorf("runtime: engine is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.newContextLocked(ctx, false)
}

func (e *Engine) newContextLocked(ctx context.Context, allowActive bool) error {
	e.threadRuntimeMu.RLock()
	current := e.threadRuntimeStateLocked()
	e.threadRuntimeMu.RUnlock()
	if current.Thread == nil {
		return fmt.Errorf("runtime: Thread is required")
	}
	if !allowActive && (e.activeTurnID != "" || len(e.pendingInput) > 0) {
		return ErrThreadRuntimeBusy
	}
	if _, err := current.Thread.BeginNewGeneration(); err != nil {
		return err
	}
	e.policyRuntimeContextMu.Lock()
	e.pendingPolicyRuntimeContext = nil
	e.policyRuntimeContextMu.Unlock()
	runtimemodule.NotifyContextRenewed(context.WithoutCancel(ctx), current.Modules, e.RuntimeModules)
	return nil
}
