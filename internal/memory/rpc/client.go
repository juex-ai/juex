// Package rpc is the private typed Memory service client.
package rpc

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/application"
	appwire "github.com/juex-ai/juex/internal/foundation/application/rpc"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	wire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/memory"
	"github.com/juex-ai/juex/internal/memory"
)

type Client struct{ client wire.Client }

func NewClient(address string, credentials platformrpc.Credentials) (*Client, error) {
	options, err := platformrpc.ClientOptions(address, "memory", credentials)
	if err != nil {
		return nil, err
	}
	value, err := wire.NewClient("memory", options...)
	if err != nil {
		return nil, err
	}
	return &Client{client: value}, nil
}
func encode(value any) string { data, _ := json.Marshal(value); return string(data) }
func (c *Client) Health(ctx context.Context) error {
	reply, err := c.client.Health(ctx)
	return platformrpc.Decode(reply, err, nil, appwire.DecodeError)
}
func (c *Client) Status(ctx context.Context, access application.Access) (memory.Status, error) {
	reply, err := c.client.Status(ctx, encode(access))
	var value memory.Status
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Search(ctx context.Context, access application.Access, query mc.Query) (mc.Page, error) {
	reply, err := c.client.Search(ctx, encode(access), encode(query))
	var value mc.Page
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Read(ctx context.Context, access application.Access, request mc.ReadRequest) (mc.Entry, error) {
	reply, err := c.client.Read(ctx, encode(access), encode(request))
	var value mc.Entry
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Facts(ctx context.Context, access application.Access, query mc.Query) (mc.FactPage, error) {
	reply, err := c.client.Facts(ctx, encode(access), encode(query))
	var value mc.FactPage
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Domains(ctx context.Context, access application.Access, request mc.DomainRequest) ([]mc.Domain, error) {
	reply, err := c.client.Domains(ctx, encode(access), encode(request))
	var value []mc.Domain
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Administer(ctx context.Context, access application.Access, request mc.AdminRequest) (mc.Receipt, error) {
	reply, err := c.client.Administer(ctx, encode(access), encode(request))
	var value mc.Receipt
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Configure(ctx context.Context, access application.Access, version int64, enabled bool, strategy string) (memory.Status, error) {
	reply, err := c.client.Configure(ctx, encode(access), version, enabled, strategy)
	var value memory.Status
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Result(ctx context.Context, access application.Access, thread, id string) (mc.Receipt, error) {
	reply, err := c.client.ReviewResult_(ctx, encode(access), thread, id)
	var value mc.Receipt
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Propose(ctx context.Context, scope application.Scope, thread string, proposal mc.Proposal, automatic bool, commandID string) (mc.Receipt, error) {
	reply, err := c.client.Propose(ctx, encode(scope), thread, encode(proposal), automatic, commandID)
	var value mc.Receipt
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Review(ctx context.Context, scope application.Scope, binding memory.Binding) (memory.Review, error) {
	reply, err := c.client.Review(ctx, encode(scope), encode(binding))
	var value memory.Review
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Decide(ctx context.Context, scope application.Scope, binding memory.Binding, decision mc.Decision, commandID string) (mc.Receipt, error) {
	reply, err := c.client.Decide(ctx, encode(scope), encode(binding), encode(decision), commandID)
	var value mc.Receipt
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}

func (c *Client) CancelCommand(ctx context.Context, scope application.Scope, id string) error {
	reply, err := c.client.CancelCommand(ctx, encode(scope), id)
	return platformrpc.Decode(reply, err, nil, appwire.DecodeError)
}
