package rpc

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/agentcontrol"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
)

func (a *Authority) ControlAgents(ctx context.Context, source agentcontrol.Source, id, after string) (agentcontrol.Page, error) {
	var value agentcontrol.Page
	encoded, err := json.Marshal(source)
	if err != nil {
		return value, err
	}
	reply, err := a.client.ControlAgents(ctx, string(encoded), id, after)
	if err != nil {
		return value, err
	}
	err = platformrpc.Decode(reply, err, &value, decodeError)
	return value, err
}
func (a *Authority) ControlAgent(ctx context.Context, source agentcontrol.Source, action agentcontrol.Action, cancel bool) (agentcontrol.Receipt, error) {
	var value agentcontrol.Receipt
	identity, err := json.Marshal(source)
	if err != nil {
		return value, err
	}
	encoded, err := json.Marshal(action)
	if err != nil {
		return value, err
	}
	reply, err := a.client.ControlAgent(ctx, string(identity), string(encoded), cancel)
	if err != nil {
		return value, err
	}
	err = platformrpc.Decode(reply, err, &value, decodeError)
	return value, err
}
