package runtime

import (
	"context"
	"errors"
	"fmt"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/thread"
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
	generationID := current.Thread.Projection().CurrentGeneration.ID
	clear, err := runtimemodule.ClearContextForRenewal(ctx, current.Modules, generationID)
	if err != nil {
		return err
	}
	if _, err := current.Thread.BeginNewGeneration(); err != nil {
		var persistErr *thread.ProjectionPersistError
		if !errors.As(err, &persistErr) {
			return errors.Join(err, clear.Rollback())
		}
		// The Journal boundary is durable even though its derived projection did
		// not persist. Keep the module clear on the committed side of the boundary.
		finalizeErr := clear.Finalize()
		e.finishContextRenewal(ctx, current.Modules)
		return errors.Join(err, finalizeErr)
	}
	finalizeErr := clear.Finalize()
	e.finishContextRenewal(ctx, current.Modules)
	if finalizeErr != nil {
		return fmt.Errorf("runtime: finalize committed Context renewal: %w", finalizeErr)
	}
	return nil
}

func (e *Engine) finishContextRenewal(ctx context.Context, modules *runtimemodule.Set) {
	e.policyRuntimeContextMu.Lock()
	e.pendingPolicyRuntimeContext = nil
	e.policyRuntimeContextMu.Unlock()
	runtimemodule.NotifyContextRenewed(context.WithoutCancel(ctx), modules, e.RuntimeModules)
}
