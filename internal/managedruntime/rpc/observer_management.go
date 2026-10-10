package rpc

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (a *Authority) ExtensionCatalog(ctx context.Context, scope managedruntime.Scope) (managedruntime.ExtensionCatalog, error) {
	encoded, err := json.Marshal(scope)
	if err != nil {
		return managedruntime.ExtensionCatalog{}, err
	}
	reply, err := a.client.ExtensionCatalog(ctx, string(encoded))
	var result managedruntime.ExtensionCatalog
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
func (c *Client) ObservationSources(ctx context.Context, user, tenant, agent, after string) (managedruntime.ObservationPage, error) {
	reply, err := c.client.ObservationSources(ctx, actor(user, tenant, agent), after)
	var result managedruntime.ObservationPage
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
func (c *Client) ObservedEvents(ctx context.Context, user, tenant, agent, source, after string) (managedruntime.ObservedEvents, error) {
	reply, err := c.client.ObservedEvents(ctx, actor(user, tenant, agent), source, after)
	var result managedruntime.ObservedEvents
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
func (c *Client) ObservationContent(ctx context.Context, user, tenant, agent, id string, offset, limit int) (managedruntime.ObservationContent, error) {
	reply, err := c.client.ObservationContent(ctx, actor(user, tenant, agent), id, int32(offset), int32(limit))
	var result managedruntime.ObservationContent
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
func (c *Client) StartObserver(ctx context.Context, user, tenant, agent string, request managedruntime.ObserverStart) (managedruntime.ObserverControl, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return managedruntime.ObserverControl{}, err
	}
	reply, err := c.client.StartObserver(ctx, actor(user, tenant, agent), string(encoded))
	var result managedruntime.ObserverControl
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
func (c *Client) StopObserver(ctx context.Context, user, tenant, agent, source string) error {
	reply, err := c.client.StopObserver(ctx, actor(user, tenant, agent), source)
	return platformrpc.Decode(reply, err, nil, decodeError)
}
func (c *Client) SetSourceSubscription(ctx context.Context, user, tenant, agent, source, thread string, enabled bool) (managedruntime.Subscription, error) {
	reply, err := c.client.SetSourceSubscription(ctx, actor(user, tenant, agent), source, thread, enabled)
	var result managedruntime.Subscription
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
