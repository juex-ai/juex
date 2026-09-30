// Package rpc resolves application ownership through private Management RPC.
package rpc

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	wire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/management"
)

type Authority struct{ client wire.Client }

func NewAuthority(address string, credentials platformrpc.Credentials) (*Authority, error) {
	options, err := platformrpc.ClientOptions(address, "management", credentials)
	if err != nil {
		return nil, err
	}
	client, err := wire.NewClient("management", options...)
	if err != nil {
		return nil, err
	}
	return &Authority{client: client}, nil
}

func (a *Authority) AuthorizeApplication(ctx context.Context, access application.Access, execute bool) (application.Scope, error) {
	encoded, err := json.Marshal(access)
	if err != nil {
		return application.Scope{}, err
	}
	reply, err := a.client.ApplicationAuthority(ctx, string(encoded), execute)
	var scope application.Scope
	err = platformrpc.Decode(reply, err, &scope, DecodeError)
	return scope, err
}

func ErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, application.ErrDenied):
		return "denied"
	case errors.Is(err, application.ErrInvalid):
		return "invalid"
	case errors.Is(err, application.ErrConflict):
		return "conflict"
	case errors.Is(err, application.ErrDisabled):
		return "disabled"
	default:
		return "unavailable"
	}
}

func DecodeError(code string) error {
	switch code {
	case "denied":
		return application.ErrDenied
	case "invalid":
		return application.ErrInvalid
	case "conflict":
		return application.ErrConflict
	case "disabled":
		return application.ErrDisabled
	default:
		return platformrpc.ErrUnavailable
	}
}
