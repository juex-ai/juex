package managedruntime

import (
	"context"
	"errors"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"time"
)

func (r *Runner) deliverEvidence(ctx context.Context) {
	store, ok := r.store.(EvidenceOutbox)
	if !ok {
		return
	}
	gateway, ok := r.config.Applications.(EvidenceGateway)
	if !ok {
		return
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		call, cancel := context.WithTimeout(ctx, 15*time.Second)
		items, err := store.PendingEvidence(call, 100)
		if err == nil {
			for _, item := range items {
				attempt, stop := context.WithTimeout(call, 2*time.Second)
				err := func() error {
					if item.Text == "" {
						return ErrInvalid
					}
					fresh, err := r.currentScope(attempt, item.Scope)
					if err != nil {
						return err
					}
					if !fresh.Capabilities.Allows(agentpolicy.Memory) {
						return ErrDenied
					}
					item.Scope.Capabilities = fresh.Capabilities
					return gateway.Contribute(attempt, item)
				}()
				reason := ""
				if errors.Is(err, ErrDenied) {
					reason = "participation unavailable or execution authority changed"
				} else if errors.Is(err, ErrInvalid) {
					reason = "original evidence exceeds application constraints"
				}
				if err == nil || reason != "" {
					_ = store.FinishEvidence(attempt, item.ID, reason)
				}
				stop()
				if call.Err() != nil {
					break
				}
			}
		}
		cancel()
	}
}
