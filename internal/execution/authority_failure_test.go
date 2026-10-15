package execution

import (
	"context"
	"errors"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type failingAuthority struct {
	ownerErr, agentErr error
}

func (a failingAuthority) Owner(context.Context, string, string, string, bool) (OwnerScope, error) {
	return OwnerScope{FleetID: "fleet"}, a.ownerErr
}

func (a failingAuthority) Agent(context.Context, string, string, string, bool) (Scope, error) {
	return Scope{OwnerScope: OwnerScope{FleetID: "fleet"}}, a.agentErr
}

func TestEffectiveGrantsDistinguishesDenialFromUnavailability(t *testing.T) {
	device := Device{Status: "active", FleetID: "fleet", Grants: map[string][]execprotocol.Capability{"agent": {execprotocol.MCP}}}
	for _, boundary := range []string{"owner", "agent"} {
		for _, failure := range []error{execprotocol.ErrDenied, execprotocol.ErrUnavailable, context.DeadlineExceeded, context.Canceled} {
			t.Run(boundary+"/"+failure.Error(), func(t *testing.T) {
				authority := failingAuthority{}
				if boundary == "owner" {
					authority.ownerErr = failure
				} else {
					authority.agentErr = failure
				}
				grants, err := (&Service{Authority: authority}).EffectiveGrants(context.Background(), device)
				if errors.Is(failure, execprotocol.ErrDenied) {
					if err != nil || len(grants) != 0 {
						t.Fatal("confirmed denial must remove grants", grants, err)
					}
				} else if !errors.Is(err, failure) || grants != nil {
					t.Fatal("unconfirmed authority became a grant decision", grants, err)
				}
			})
		}
	}
}

type partiallyUnavailableAuthority struct {
	failingAuthority
	queried int
}

func (a *partiallyUnavailableAuthority) Agent(context.Context, string, string, string, bool) (Scope, error) {
	a.queried++
	if a.queried == 2 {
		return Scope{}, execprotocol.ErrUnavailable
	}
	return Scope{OwnerScope: OwnerScope{FleetID: "fleet"}}, nil
}

func TestEffectiveGrantsDoesNotPublishPartialSnapshot(t *testing.T) {
	authority := &partiallyUnavailableAuthority{}
	device := Device{Status: "active", FleetID: "fleet", Grants: map[string][]execprotocol.Capability{"one": {execprotocol.MCP}, "two": {execprotocol.MCP}}}
	grants, err := (&Service{Authority: authority}).EffectiveGrants(context.Background(), device)
	if authority.queried != 2 || grants != nil || !errors.Is(err, execprotocol.ErrUnavailable) {
		t.Fatal("partial grants would revoke the second Agent", grants, err)
	}
}

type authorityFailureRepository struct {
	Repository
	device    Device
	cancelled bool
}

func (s *authorityFailureRepository) Device(context.Context, string) (Device, error) {
	return s.device, nil
}
func (s *authorityFailureRepository) Touch(context.Context, string, int64, bool) error { return nil }
func (s *authorityFailureRepository) CancelOperation(context.Context, string, string) error {
	s.cancelled = true
	return nil
}

func TestBrokerAuthorityFailureDoesNotRevokeOrDispatch(t *testing.T) {
	store := &authorityFailureRepository{device: Device{Status: "active", FleetID: "fleet"}}
	service := &Service{Store: store, Authority: failingAuthority{ownerErr: execprotocol.ErrUnavailable}}
	exchanged := false
	exchange := func(context.Context, execprotocol.Envelope) (execprotocol.Envelope, error) {
		exchanged = true
		return execprotocol.Envelope{}, execprotocol.ErrUnavailable
	}
	if err := service.Connected(context.Background(), store.device, exchange); !errors.Is(err, execprotocol.ErrUnavailable) || exchanged {
		t.Fatal("authority failure sent an invented grants update", err, exchanged)
	}
	service.Authority = failingAuthority{agentErr: execprotocol.ErrUnavailable}
	for _, state := range []string{"waiting", "running"} {
		operation := Operation{State: state}
		if err := service.reconcileDeviceOperation(context.Background(), store.device, nil, operation, exchange); !errors.Is(err, execprotocol.ErrUnavailable) || store.cancelled || exchanged {
			t.Fatal("authority failure changed the operation", state, err, store.cancelled, exchanged)
		}
	}
	service.Authority = failingAuthority{agentErr: execprotocol.ErrDenied}
	if err := service.reconcileDeviceOperation(context.Background(), store.device, nil, Operation{State: "waiting"}, exchange); err != nil || !store.cancelled || exchanged {
		t.Fatal("confirmed denial did not cancel queued work", err, store.cancelled, exchanged)
	}
}
