package rpc

import (
	"context"
	"encoding/json"
	appwire "github.com/juex-ai/juex/internal/foundation/application/rpc"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
)

func (c *Client) Purge(ctx context.Context, request lifecycle.Request) (lifecycle.Receipt, error) {
	encoded, _ := json.Marshal(request)
	reply, err := c.client.Purge(ctx, string(encoded))
	var result lifecycle.Receipt
	err = platformrpc.Decode(reply, err, &result, appwire.DecodeError)
	return result, err
}
