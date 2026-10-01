package execution

import (
	"context"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// Prepared cancellation is restricted to Runtime's durable work identities.
// The state confirms settlement only when terminal. A recorded request can
// precede admission or an executor's acknowledgement by an arbitrary interval.
func (s *Service) CancelPreparedOperation(ctx context.Context, actor, tenant, agent, environment, id string) (execprotocol.State, error) {
	if _, err := uuid.Parse(id); err != nil {
		return "", execprotocol.ErrInvalid
	}
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, false)
	if err != nil {
		return "", err
	}
	return s.Store.CancelPreparedOperation(ctx, scope, environment, id)
}

func (s *Service) CancelPreparedTransfer(ctx context.Context, actor, tenant, agent, requestID string) (execprotocol.State, error) {
	if s.Transfers == nil {
		return "", execprotocol.ErrUnavailable
	}
	if _, err := uuid.Parse(requestID); err != nil {
		return "", execprotocol.ErrInvalid
	}
	scope, err := s.Authority.Agent(ctx, actor, tenant, agent, false)
	if err != nil {
		return "", err
	}
	return s.Transfers.CancelPreparedTransfer(ctx, scope, TransferID(agent, requestID))
}
