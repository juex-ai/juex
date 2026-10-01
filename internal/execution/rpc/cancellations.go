package rpc

import (
	"context"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func (c *Client) CancelPreparedOperation(ctx context.Context, user, tenant, agent, environment, request string) (execprotocol.State, error) {
	reply, err := c.client.CancelPreparedOperation(ctx, actor(user, tenant, agent), environment, request)
	var state execprotocol.State
	err = decode(reply, err, &state)
	return state, err
}

func (c *Client) CancelPreparedTransfer(ctx context.Context, user, tenant, agent, request string) (execprotocol.State, error) {
	reply, err := c.client.CancelPreparedTransfer(ctx, actor(user, tenant, agent), request)
	var state execprotocol.State
	err = decode(reply, err, &state)
	return state, err
}
