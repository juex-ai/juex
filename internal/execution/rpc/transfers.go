package rpc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func (c *Client) BeginTransfer(ctx context.Context, user, tenant, agent string, request execution.TransferRequest) (execution.Transfer, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return execution.Transfer{}, err
	}
	reply, err := c.client.BeginTransfer(ctx, actor(user, tenant, agent), string(encoded))
	var result execution.Transfer
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) BeginTransferFenced(ctx context.Context, user, tenant, agent string, request execution.TransferRequest, fence execprotocol.AuthorityFence) (execution.Transfer, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return execution.Transfer{}, err
	}
	encodedFence, err := json.Marshal(fence)
	if err != nil {
		return execution.Transfer{}, err
	}
	reply, err := c.client.BeginTransferFenced(ctx, actor(user, tenant, agent), string(encoded), string(encodedFence))
	var result execution.Transfer
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) Transfer(ctx context.Context, user, tenant, agent, id string) (execution.Transfer, error) {
	reply, err := c.client.Transfer(ctx, actor(user, tenant, agent), id)
	var result execution.Transfer
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) ListTransfers(ctx context.Context, user, tenant, agent, after string, limit int) ([]execution.Transfer, error) {
	if limit < 1 || limit > 100 {
		return nil, execprotocol.ErrInvalid
	}
	reply, err := c.client.ListTransfers(ctx, actor(user, tenant, agent), after, int32(limit))
	var result []execution.Transfer
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) CancelTransfer(ctx context.Context, user, tenant, agent, id string) error {
	reply, err := c.client.CancelTransfer(ctx, actor(user, tenant, agent), id)
	return decode(reply, err, nil)
}

func (c *Client) ExtendTransfer(ctx context.Context, user, tenant, agent, id string, wait time.Duration) error {
	reply, err := c.client.ExtendTransfer(ctx, actor(user, tenant, agent), id, wait.Milliseconds())
	return decode(reply, err, nil)
}
