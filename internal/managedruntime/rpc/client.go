// Package rpc adapts Runtime business contracts to the private Kitex services.
package rpc

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	managementwire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/management"
	runtimewire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/runtime"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func ErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, managedruntime.ErrDenied):
		return "denied"
	case errors.Is(err, managedruntime.ErrInvalid):
		return "invalid"
	case errors.Is(err, managedruntime.ErrConflict):
		return "conflict"
	case errors.Is(err, managedruntime.ErrPaused):
		return "paused"
	case errors.Is(err, managedruntime.ErrSourceBusy):
		return "source_busy"
	case errors.Is(err, managedruntime.ErrModelUnavailable):
		return "model_unavailable"
	case errors.Is(err, managedruntime.ErrMediaUnavailable):
		return "media_unavailable"
	default:
		return "unavailable"
	}
}
func decodeError(code string) error {
	switch code {
	case "denied":
		return managedruntime.ErrDenied
	case "invalid":
		return managedruntime.ErrInvalid
	case "conflict":
		return managedruntime.ErrConflict
	case "paused":
		return managedruntime.ErrPaused
	case "source_busy":
		return managedruntime.ErrSourceBusy
	case "model_unavailable":
		return managedruntime.ErrModelUnavailable
	case "media_unavailable":
		return managedruntime.ErrMediaUnavailable
	default:
		return platformrpc.ErrUnavailable
	}
}
func actor(user, tenant, agent string) *platform.Actor {
	return &platform.Actor{UserID: user, TenantID: tenant, AgentID: agent}
}

type Client struct{ client runtimewire.Client }

func (a *Authority) RecordNotification(ctx context.Context, event application.Event) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	reply, err := a.client.RecordNotification(ctx, string(data))
	return platformrpc.Decode(reply, err, nil, decodeError)
}

func NewClient(address string, credentials platformrpc.Credentials) (*Client, error) {
	opts, err := platformrpc.ClientOptions(address, "runtime", credentials)
	if err != nil {
		return nil, err
	}
	client, err := runtimewire.NewClient("runtime", opts...)
	if err != nil {
		return nil, err
	}
	return &Client{client: client}, nil
}
func (c *Client) Health(ctx context.Context) error {
	reply, err := c.client.Health(ctx)
	return platformrpc.Decode(reply, err, nil, decodeError)
}
func (c *Client) Submit(ctx context.Context, user, tenant, agent string, input managedruntime.InputRequest) (managedruntime.InputReceipt, error) {
	data, err := json.Marshal(input)
	if err != nil {
		return managedruntime.InputReceipt{}, err
	}
	reply, err := c.client.Submit(ctx, actor(user, tenant, agent), string(data))
	var result managedruntime.InputReceipt
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
func (c *Client) Threads(ctx context.Context, user, tenant, agent string) ([]managedruntime.Thread, error) {
	reply, err := c.client.Threads(ctx, actor(user, tenant, agent))
	var result []managedruntime.Thread
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
func (c *Client) Status(ctx context.Context, user, tenant, agent string) (managedruntime.RuntimeStatus, error) {
	reply, err := c.client.Status(ctx, actor(user, tenant, agent))
	var result managedruntime.RuntimeStatus
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}

func (c *Client) InputChecks(ctx context.Context, user, tenant, agent, thread string, query managedruntime.InputCheckQuery) (managedruntime.InputCheckPage, error) {
	if err := query.Validate(); err != nil {
		return managedruntime.InputCheckPage{}, err
	}
	encoded, err := json.Marshal(query)
	if err != nil {
		return managedruntime.InputCheckPage{}, err
	}
	reply, err := c.client.InputChecks(ctx, actor(user, tenant, agent), thread, string(encoded))
	var result managedruntime.InputCheckPage
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}

func (c *Client) Inspection(ctx context.Context, user, tenant, agent, thread string) (managedruntime.ThreadInspection, error) {
	reply, err := c.client.Inspection(ctx, actor(user, tenant, agent), thread)
	var result managedruntime.ThreadInspection
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}

func (c *Client) History(ctx context.Context, user, tenant, agent, thread string, before int64, limit int) (managedruntime.Timeline, error) {
	if before < 0 || limit < 1 || limit > 500 {
		return managedruntime.Timeline{}, managedruntime.ErrInvalid
	}
	reply, err := c.client.History(ctx, actor(user, tenant, agent), thread, before, int32(limit))
	var result managedruntime.Timeline
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}

func (c *Client) Events(ctx context.Context, user, tenant, agent, thread string, after int64, limit int) (managedruntime.Timeline, error) {
	if limit < 1 || limit > 500 {
		return managedruntime.Timeline{}, managedruntime.ErrInvalid
	}
	reply, err := c.client.Timeline(ctx, actor(user, tenant, agent), thread, after, int32(limit))
	var result managedruntime.Timeline
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
func (c *Client) Cancel(ctx context.Context, user, tenant, agent, thread string) error {
	reply, err := c.client.Cancel(ctx, actor(user, tenant, agent), thread)
	return platformrpc.Decode(reply, err, nil, decodeError)
}
func (c *Client) Worker(ctx context.Context, user, tenant, agent, parent, requestID, name string) (managedruntime.Thread, error) {
	reply, err := c.client.CreateWorker(ctx, actor(user, tenant, agent), parent, requestID, name)
	var result managedruntime.Thread
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}

type Authority struct {
	client  managementwire.Client
	factory func(llm.ProviderProfile) (llm.Provider, error)
}

func NewAuthority(address string, credentials platformrpc.Credentials, factory func(llm.ProviderProfile) (llm.Provider, error)) (*Authority, error) {
	opts, err := platformrpc.ClientOptions(address, "management", credentials)
	if err != nil {
		return nil, err
	}
	client, err := managementwire.NewClient("management", opts...)
	if err != nil {
		return nil, err
	}
	return &Authority{client: client, factory: factory}, nil
}
func (a *Authority) Authorize(ctx context.Context, user, tenant, agent string, execute bool) (managedruntime.Scope, error) {
	reply, err := a.client.Authorize(ctx, actor(user, tenant, agent), execute)
	var result managedruntime.Scope
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
func (a *Authority) Snapshot(ctx context.Context, scope managedruntime.Scope) (managedruntime.TurnConfig, error) {
	encoded, err := json.Marshal(scope)
	if err != nil {
		return managedruntime.TurnConfig{}, err
	}
	reply, err := a.client.Snapshot(ctx, string(encoded))
	var result managedruntime.TurnConfig
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
func (a *Authority) Provider(ctx context.Context, scope managedruntime.Scope, config managedruntime.ModelConfig, requirements managedruntime.ModelRequirements) (llm.Provider, error) {
	encodedScope, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	encodedConfig, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	reply, err := a.client.ModelProfile(ctx, string(encodedScope), string(encodedConfig))
	var profile llm.ProviderProfile
	if err := platformrpc.Decode(reply, err, &profile, decodeError); err != nil {
		return nil, err
	}
	if err := requirements.Check(profile); err != nil {
		return nil, err
	}
	return a.factory(profile)
}

func (c *Client) Compact(ctx context.Context, user, tenant, agent, thread string, request managedruntime.CompactionRequest) (managedruntime.InputReceipt, error) {
	reply, err := c.client.Compact(ctx, actor(user, tenant, agent), thread, request.RequestID, request.Focus)
	var result managedruntime.InputReceipt
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}

func (c *Client) ResetContext(ctx context.Context, user, tenant, agent, thread, requestID string) (managedruntime.Thread, error) {
	reply, err := c.client.ResetContext(ctx, actor(user, tenant, agent), thread, requestID)
	var result managedruntime.Thread
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}

func (a *Authority) Peers(ctx context.Context, scope managedruntime.Scope) ([]managedruntime.PeerAgent, error) {
	encoded, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	reply, err := a.client.Peers(ctx, string(encoded))
	var result []managedruntime.PeerAgent
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}

func (c *Client) Archive(ctx context.Context, user, tenant, agent, thread string, archived bool) (managedruntime.Thread, error) {
	reply, err := c.client.Archive(ctx, actor(user, tenant, agent), thread, archived)
	var result managedruntime.Thread
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}

func (c *Client) DeleteThread(ctx context.Context, user, tenant, agent, thread string) (managedruntime.ThreadDeletionReceipt, error) {
	reply, err := c.client.DeleteThread(ctx, actor(user, tenant, agent), thread)
	var result managedruntime.ThreadDeletionReceipt
	err = platformrpc.Decode(reply, err, &result, decodeError)
	return result, err
}
