package rpc

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (c *Client) AdmitMainTrigger(ctx context.Context, scope managedruntime.Scope, q managedruntime.MainTrigger) (managedruntime.MainTriggerReceipt, error) {
	encodedScope, _ := json.Marshal(scope)
	encodedRequest, _ := json.Marshal(q)
	reply, err := c.client.AdmitMainTrigger(ctx, string(encodedScope), string(encodedRequest))
	var value managedruntime.MainTriggerReceipt
	err = platformrpc.Decode(reply, err, &value, decodeError)
	return value, err
}
func (c *Client) MainTriggerReceipt(ctx context.Context, scope managedruntime.Scope, id string) (managedruntime.MainTriggerReceipt, error) {
	encoded, _ := json.Marshal(scope)
	reply, err := c.client.MainTriggerReceipt(ctx, string(encoded), id)
	var value managedruntime.MainTriggerReceipt
	err = platformrpc.Decode(reply, err, &value, decodeError)
	return value, err
}
func (c *Client) CancelMainTrigger(ctx context.Context, scope managedruntime.Scope, id string) (managedruntime.MainTriggerReceipt, error) {
	encoded, _ := json.Marshal(scope)
	reply, err := c.client.CancelMainTrigger(ctx, string(encoded), id)
	var value managedruntime.MainTriggerReceipt
	err = platformrpc.Decode(reply, err, &value, decodeError)
	return value, err
}
