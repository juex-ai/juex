package rpc

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (c *Client) AgentRunState(ctx context.Context, user, tenant, agent string) (managedruntime.AgentRunState, error) {
	reply, err := c.client.AgentRunState(ctx, actor(user, tenant, agent))
	var value managedruntime.AgentRunState
	err = platformrpc.Decode(reply, err, &value, decodeError)
	return value, err
}
func (c *Client) ChangeAgentLifecycle(ctx context.Context, user, tenant, agent string, change managedruntime.AgentLifecycleChange) (managedruntime.AgentLifecycleReceipt, error) {
	encoded, err := json.Marshal(change)
	if err != nil {
		return managedruntime.AgentLifecycleReceipt{}, err
	}
	reply, err := c.client.ChangeAgentLifecycle(ctx, actor(user, tenant, agent), string(encoded))
	var value managedruntime.AgentLifecycleReceipt
	err = platformrpc.Decode(reply, err, &value, decodeError)
	return value, err
}
