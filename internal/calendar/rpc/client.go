// Package rpc provides the typed private Calendar SDK.
package rpc

import (
	"context"
	"encoding/json"

	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/foundation/application"
	appwire "github.com/juex-ai/juex/internal/foundation/application/rpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	wire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/calendar"
)

type Client struct{ client wire.Client }

func NewClient(address string, credentials platformrpc.Credentials) (*Client, error) {
	options, err := platformrpc.ClientOptions(address, "calendar", credentials)
	if err != nil {
		return nil, err
	}
	client, err := wire.NewClient("calendar", options...)
	if err != nil {
		return nil, err
	}
	return &Client{client: client}, nil
}
func encode(value any) string { data, _ := json.Marshal(value); return string(data) }
func (c *Client) Health(ctx context.Context) error {
	reply, err := c.client.Health(ctx)
	return platformrpc.Decode(reply, err, nil, appwire.DecodeError)
}
func (c *Client) Status(ctx context.Context, access application.Access) (calendar.Status, error) {
	reply, err := c.client.Status(ctx, encode(access))
	var value calendar.Status
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Configure(ctx context.Context, access application.Access, version int64, enabled bool) (calendar.Status, error) {
	reply, err := c.client.Configure(ctx, encode(access), version, enabled)
	var value calendar.Status
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Schedules(ctx context.Context, access application.Access, offset, limit int) (calendar.SchedulePage, error) {
	if offset < 0 || offset > 1<<30 || limit < 1 || limit > 50 {
		return calendar.SchedulePage{}, application.ErrInvalid
	}
	reply, err := c.client.Schedules(ctx, encode(access), int32(offset), int32(limit))
	var value calendar.SchedulePage
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Occurrences(ctx context.Context, access application.Access, id string, offset, limit int) (calendar.OccurrencePage, error) {
	if offset < 0 || offset > 1<<30 || limit < 1 || limit > 50 {
		return calendar.OccurrencePage{}, application.ErrInvalid
	}
	reply, err := c.client.Occurrences(ctx, encode(access), id, int32(offset), int32(limit))
	var value calendar.OccurrencePage
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Change(ctx context.Context, access application.Access, scope *application.Scope, id string, q calendar.Change) (calendar.Receipt, error) {
	reply, err := c.client.Change(ctx, encode(access), encode(scope), id, encode(q))
	var value calendar.Receipt
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) Assignment(ctx context.Context, scope application.Scope, id string, epoch int64) (calendar.Delivery, error) {
	reply, err := c.client.Assignment(ctx, encode(scope), id, epoch)
	var value calendar.Delivery
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
func (c *Client) CancelCommand(ctx context.Context, scope application.Scope, id string) error {
	reply, err := c.client.CancelCommand(ctx, encode(scope), id)
	return platformrpc.Decode(reply, err, nil, appwire.DecodeError)
}

func (c *Client) AssignTrigger(ctx context.Context, scope application.Scope, id string, epoch int64) (calendar.Delivery, error) {
	reply, err := c.client.AssignTrigger(ctx, encode(scope), id, epoch)
	var value calendar.Delivery
	err = platformrpc.Decode(reply, err, &value, appwire.DecodeError)
	return value, err
}
