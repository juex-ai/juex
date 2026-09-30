package rpc

import "context"

func (c *Client) CancelPreparedOperation(ctx context.Context, user, tenant, agent, environment, request string) error {
	reply, err := c.client.CancelPreparedOperation(ctx, actor(user, tenant, agent), environment, request)
	return decode(reply, err, nil)
}

func (c *Client) CancelPreparedTransfer(ctx context.Context, user, tenant, agent, request string) error {
	reply, err := c.client.CancelPreparedTransfer(ctx, actor(user, tenant, agent), request)
	return decode(reply, err, nil)
}
