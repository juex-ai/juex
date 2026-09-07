package app

import (
	"context"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/agent"
	"github.com/juex-ai/juex/internal/framework/runtime"
)

// DeliverExternalInput holds the target Thread lease through preparation and durable admission.
func (a *App) DeliverExternalInput(ctx context.Context, prepare func() (llm.Message, runtime.PendingInputOptions, error)) (agent.ExternalDelivery, error) {
	if a == nil || a.Engine == nil {
		return agent.ExternalDelivery{}, nil
	}
	threadLease := a.acquireExternalInputThreadLease()
	defer threadLease.Release()
	a.threadMu.RLock()
	defer a.threadMu.RUnlock()
	targetThread := ""
	if a.Thread != nil {
		targetThread = a.Thread.ID
	}
	select {
	case <-ctx.Done():
		return agent.ExternalDelivery{}, ctx.Err()
	default:
	}
	message, options, err := prepare()
	if err != nil {
		return agent.ExternalDelivery{}, err
	}
	delivery, err := a.deliverExternalInputLocked(ctx, message, options, threadLease, true, nil)
	return agent.ExternalDelivery{RecordID: options.ID, TargetThread: targetThread, Queued: delivery.Queued, Delivered: delivery.Delivered}, err
}
