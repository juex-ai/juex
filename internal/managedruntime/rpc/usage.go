package rpc

import (
	"context"
	"encoding/json"

	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (a *Authority) AuthorizeUsage(ctx context.Context, actor, tenant, owner string) error {
	reply, err := a.client.AuthorizeUsage(ctx, actor, tenant, owner)
	return platformrpc.Decode(reply, err, nil, decodeError)
}

func (c *Client) Usage(ctx context.Context, actor string, query managedruntime.UsageQuery) (managedruntime.UsageReport, error) {
	var v managedruntime.UsageReport
	data, err := json.Marshal(query)
	if err != nil {
		return v, err
	}
	reply, err := c.client.Usage(ctx, actor, string(data))
	err = platformrpc.Decode(reply, err, &v, decodeError)
	return v, err
}

func (c *Client) OperatorUsage(ctx context.Context, query managedruntime.UsageQuery) (managedruntime.UsageReport, error) {
	var v managedruntime.UsageReport
	data, err := json.Marshal(query)
	if err != nil {
		return v, err
	}
	reply, err := c.client.OperatorUsage(ctx, string(data))
	err = platformrpc.Decode(reply, err, &v, decodeError)
	return v, err
}

func (c *Client) ConfigureUsage(ctx context.Context, policy managedruntime.UsagePolicy) (managedruntime.UsagePeriod, error) {
	var v managedruntime.UsagePeriod
	data, err := json.Marshal(policy)
	if err != nil {
		return v, err
	}
	reply, err := c.client.ConfigureUsage(ctx, string(data))
	err = platformrpc.Decode(reply, err, &v, decodeError)
	return v, err
}
