package agent

import (
	"context"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/framework/runtime"
)

// DeliverExternalInput holds the target Thread lease through preparation and durable admission.
func (a *Agent) DeliverExternalInput(ctx context.Context, prepare func() (llm.Message, runtime.PendingInputOptions, error)) (ExternalDelivery, error) {
	if a == nil || a.Engine == nil {
		return ExternalDelivery{}, nil
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
		return ExternalDelivery{}, ctx.Err()
	default:
	}
	message, options, err := prepare()
	if err != nil {
		return ExternalDelivery{}, err
	}
	delivery, err := a.deliverExternalInputLocked(ctx, message, options, threadLease, true, nil)
	return ExternalDelivery{RecordID: options.ID, TargetThread: targetThread, Queued: delivery.Queued, Delivered: delivery.Delivered}, err
}
