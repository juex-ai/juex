package module

import (
	"context"
	"fmt"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
)

// InputPreparationRequest identifies already admitted input. Preparation may
// freeze optional external context; it cannot replace the original message.
type InputPreparationRequest struct {
	Runtime       RuntimeContext
	Thread        *ThreadContext
	TurnID        string
	InputID       string
	PreparationID string
	Message       llm.Message
	Budget        time.Duration
	Observer      PolicyObserver
}

type InputPreparer interface {
	PrepareInput(context.Context, InputPreparationRequest) error
}

func PrepareInputs(ctx context.Context, request InputPreparationRequest, sets ...*Set) error {
	for _, set := range sets {
		if set == nil {
			continue
		}
		if err := set.prepareInput(ctx, request); err != nil {
			return err
		}
	}
	return nil
}

func (s *Set) prepareInput(ctx context.Context, request InputPreparationRequest) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.state.closed {
		return fmt.Errorf("runtime modules: %s set is closed", s.scope)
	}
	for _, registered := range s.modules {
		preparer, ok := registered.module.(InputPreparer)
		if !ok {
			continue
		}
		owned := request
		owned.Message = cloneMessage(request.Message)
		owned.Observer = ownedPolicyObserver{owner: registered.id, point: PolicyPointInputPreparation, next: request.Observer}
		if err := preparer.PrepareInput(ctx, owned); err != nil {
			return fmt.Errorf("runtime module %q input preparation: %w", registered.id, err)
		}
	}
	return nil
}
