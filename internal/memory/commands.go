package memory

import (
	"github.com/juex-ai/juex/internal/foundation/application"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

type CommandReceipt struct {
	Scope       application.Scope `json:"scope"`
	Fingerprint string            `json:"fingerprint,omitempty"`
	Cancelled   bool              `json:"cancelled"`
	Receipt     mc.Receipt        `json:"receipt"`
}

func commandKey(scope application.Scope, id string) (string, error) {
	if !scope.Valid() || scope.AgentID == "" || id == "" || len(id) > 200 {
		return "", application.ErrInvalid
	}
	return scope.AgentID + "/" + id, nil
}

// Command acceptance and the business mutation share the Fleet transaction.
// Cancellation can arrive before an RPC first reaches Memory, but cannot undo
// an already accepted proposal or a committed review decision.
func (s *State) command(scope application.Scope, id string, payload any, apply func() (mc.Receipt, error)) (mc.Receipt, error) {
	key, err := commandKey(scope, id)
	if err != nil {
		return mc.Receipt{}, err
	}
	hash := digest(payload)
	if prior, ok := s.Commands[key]; ok {
		if !prior.Scope.SameAuthority(scope) || prior.Cancelled {
			return mc.Receipt{}, application.ErrDenied
		}
		if prior.Fingerprint != hash {
			return mc.Receipt{}, application.ErrConflict
		}
		return prior.Receipt, nil
	}
	receipt, err := apply()
	if err != nil {
		return mc.Receipt{}, err
	}
	s.Commands[key] = CommandReceipt{Scope: scope, Fingerprint: hash, Receipt: receipt}
	return receipt, nil
}

func (s *State) CancelCommand(scope application.Scope, id string) error {
	key, err := commandKey(scope, id)
	if err != nil {
		return err
	}
	if prior, ok := s.Commands[key]; ok {
		if !prior.Scope.SameAuthority(scope) {
			return application.ErrDenied
		}
		return nil
	}
	s.Commands[key] = CommandReceipt{Scope: scope, Cancelled: true}
	return nil
}
