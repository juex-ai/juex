// Package rpc adapts the Execution API to private Kitex transport.
package rpc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform"
	executionwire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/execution"
)

type Client struct{ client executionwire.Client }

func NewClient(address string, credentials platformrpc.Credentials) (*Client, error) {
	opts, err := platformrpc.ClientOptions(address, "execution", credentials)
	if err != nil {
		return nil, err
	}
	client, err := executionwire.NewClient("execution", opts...)
	if err != nil {
		return nil, err
	}
	return &Client{client: client}, nil
}
func decode(reply *platform.Reply, err error, target any) error {
	return platformrpc.Decode(reply, err, target, execprotocol.FromErrorCode)
}
func actor(user, tenant, agent string) *platform.Actor {
	return &platform.Actor{UserID: user, TenantID: tenant, AgentID: agent}
}
func (c *Client) Health(ctx context.Context) error {
	reply, err := c.client.Health(ctx)
	return decode(reply, err, nil)
}
func (c *Client) PreviewPair(ctx context.Context, actor, tenant, pair string) (execution.Pairing, error) {
	reply, err := c.client.PreviewPair(ctx, actor, tenant, pair)
	var result execution.Pairing
	err = decode(reply, err, &result)
	return result, err
}
func (c *Client) ApprovePair(ctx context.Context, actor, tenant, pair string, grants map[string][]execprotocol.Capability) (execution.Pairing, error) {
	encoded, err := json.Marshal(grants)
	if err != nil {
		return execution.Pairing{}, err
	}
	reply, err := c.client.ApprovePair(ctx, actor, tenant, pair, string(encoded))
	var result execution.Pairing
	err = decode(reply, err, &result)
	return result, err
}
func (c *Client) Devices(ctx context.Context, actor, tenant, owner string) ([]execution.Device, error) {
	reply, err := c.client.Devices(ctx, actor, tenant, owner)
	var result []execution.Device
	err = decode(reply, err, &result)
	return result, err
}
func (c *Client) Restrict(ctx context.Context, actor, tenant, environment string, version int64, grants map[string][]execprotocol.Capability) (execution.Device, error) {
	encoded, err := json.Marshal(grants)
	if err != nil {
		return execution.Device{}, err
	}
	reply, err := c.client.Restrict(ctx, actor, tenant, environment, version, string(encoded))
	var result execution.Device
	err = decode(reply, err, &result)
	return result, err
}
func (c *Client) Revoke(ctx context.Context, actor, tenant, environment string) error {
	reply, err := c.client.Revoke(ctx, actor, tenant, environment)
	return decode(reply, err, nil)
}
func (c *Client) Environments(ctx context.Context, user, tenant, agent string) ([]execprotocol.Environment, error) {
	reply, err := c.client.Environments(ctx, actor(user, tenant, agent))
	var result []execprotocol.Environment
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) InspectEnvironments(ctx context.Context, user, tenant, agent string) (execution.EnvironmentInspection, error) {
	reply, err := c.client.InspectEnvironments(ctx, actor(user, tenant, agent))
	var result execution.EnvironmentInspection
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) InspectMCP(ctx context.Context, user, tenant, agent, after string) (execution.MCPPage, error) {
	reply, err := c.client.InspectMCP(ctx, actor(user, tenant, agent), after)
	var result execution.MCPPage
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) RefreshMCPTools(ctx context.Context, user, tenant, agent, environment, connection string, change execution.MCPRefresh) (*execution.MCPToolList, error) {
	encoded, err := json.Marshal(change)
	if err != nil {
		return nil, err
	}
	reply, err := c.client.RefreshMCPTools(ctx, actor(user, tenant, agent), environment, connection, string(encoded))
	var result execution.MCPToolList
	err = decode(reply, err, &result)
	return &result, err
}

func (c *Client) DefaultEnvironment(ctx context.Context, user, tenant, agent string) (execution.DefaultEnvironment, error) {
	reply, err := c.client.DefaultEnvironment(ctx, actor(user, tenant, agent))
	var result execution.DefaultEnvironment
	err = decode(reply, err, &result)
	return result, err
}

func (c *Client) SetDefaultEnvironment(ctx context.Context, user, tenant, agent string, value execution.DefaultEnvironment) (execution.DefaultEnvironment, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return execution.DefaultEnvironment{}, err
	}
	reply, err := c.client.SetDefaultEnvironment(ctx, actor(user, tenant, agent), string(encoded))
	var result execution.DefaultEnvironment
	err = decode(reply, err, &result)
	return result, err
}
func (c *Client) Submit(ctx context.Context, user, tenant, environment string, request execprotocol.Request, wait time.Duration) (execution.Operation, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return execution.Operation{}, err
	}
	reply, err := c.client.Submit(ctx, actor(user, tenant, request.AgentID), environment, string(encoded), wait.Milliseconds())
	var result execution.Operation
	err = decode(reply, err, &result)
	return result, err
}
func (c *Client) SubmitFenced(ctx context.Context, user, tenant, environment string, request execprotocol.Request, wait time.Duration, fence execprotocol.AuthorityFence) (execution.Operation, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return execution.Operation{}, err
	}
	encodedFence, err := json.Marshal(fence)
	if err != nil {
		return execution.Operation{}, err
	}
	reply, err := c.client.SubmitFenced(ctx, actor(user, tenant, request.AgentID), environment, string(encoded), wait.Milliseconds(), string(encodedFence))
	var result execution.Operation
	err = decode(reply, err, &result)
	return result, err
}
func (c *Client) Events(ctx context.Context, limit int) ([]execprotocol.Event, error) {
	if limit < 1 || limit > 500 {
		return nil, execprotocol.ErrInvalid
	}
	reply, err := c.client.Events(ctx, int32(limit))
	var result []execprotocol.Event
	err = decode(reply, err, &result)
	return result, err
}
func (c *Client) AcknowledgeEvents(ctx context.Context, ids []string) error {
	reply, err := c.client.AcknowledgeEvents(ctx, ids)
	return decode(reply, err, nil)
}
func (c *Client) AcknowledgeOutput(ctx context.Context, user, tenant, agent, environment, id string, cursor int64) error {
	reply, err := c.client.AcknowledgeOutput(ctx, actor(user, tenant, agent), environment, id, cursor)
	return decode(reply, err, nil)
}
func (c *Client) Operation(ctx context.Context, user, tenant, agent, environment, id string, cursor int64, limit int) (execution.Operation, error) {
	if limit < 1 || limit > 256<<10 {
		return execution.Operation{}, execprotocol.ErrInvalid
	}
	reply, err := c.client.Operation(ctx, actor(user, tenant, agent), environment, id, cursor, int32(limit))
	var result execution.Operation
	err = decode(reply, err, &result)
	return result, err
}
func (c *Client) Cancel(ctx context.Context, user, tenant, agent, environment, id string) error {
	reply, err := c.client.Cancel(ctx, actor(user, tenant, agent), environment, id)
	return decode(reply, err, nil)
}
func (c *Client) Extend(ctx context.Context, user, tenant, agent, environment, id string, wait time.Duration) error {
	reply, err := c.client.Extend(ctx, actor(user, tenant, agent), environment, id, wait.Milliseconds())
	return decode(reply, err, nil)
}
