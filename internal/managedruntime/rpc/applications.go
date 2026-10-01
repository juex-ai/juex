package rpc

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/application"

	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (c *Client) RecordApplicationNotice(ctx context.Context, event application.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	reply, err := c.client.RecordApplicationNotice(ctx, string(data))
	return platformrpc.Decode(reply, err, nil, decodeError)
}

func (c *Client) AdmitApplication(ctx context.Context, scope managedruntime.Scope, job managedruntime.ApplicationJob) (managedruntime.ApplicationReceipt, error) {
	scopeJSON, _ := json.Marshal(scope)
	jobJSON, _ := json.Marshal(job)
	reply, err := c.client.AdmitApplication(ctx, string(scopeJSON), string(jobJSON))
	var value managedruntime.ApplicationReceipt
	err = platformrpc.Decode(reply, err, &value, decodeError)
	return value, err
}

func (c *Client) ApplicationReceipt(ctx context.Context, scope managedruntime.Scope, application, id string) (managedruntime.ApplicationReceipt, error) {
	scopeJSON, _ := json.Marshal(scope)
	reply, err := c.client.ApplicationReceipt(ctx, string(scopeJSON), application, id)
	var value managedruntime.ApplicationReceipt
	err = platformrpc.Decode(reply, err, &value, decodeError)
	return value, err
}

func (c *Client) CancelApplication(ctx context.Context, scope managedruntime.Scope, application, id string) error {
	scopeJSON, _ := json.Marshal(scope)
	reply, err := c.client.CancelApplication(ctx, string(scopeJSON), application, id)
	return platformrpc.Decode(reply, err, nil, decodeError)
}
