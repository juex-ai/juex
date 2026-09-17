package memoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/cloudwego/kitex/server"
	"github.com/juex-ai/juex/internal/foundation/memoryclient/wire/memorywire"
	"github.com/juex-ai/juex/internal/foundation/memoryclient/wire/memorywire/memory"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

const rpcBudget = 2 * time.Second
const maxPayload = 256 * 1024

type Client struct {
	resolver serviceendpoint.Resolver
	service  string
	caller   Caller
}

func New(resolver serviceendpoint.Resolver, service string, caller Caller) *Client {
	return &Client{resolver: resolver, service: service, caller: caller}
}
func (c *Client) Caller() Caller { return c.caller }
func (c *Client) call(ctx context.Context, caller Caller, method string, payload any, output any) error {
	if caller != c.caller {
		return errors.New("memory client caller is frozen at construction")
	}
	ctx, cancel := context.WithTimeout(ctx, rpcBudget)
	defer cancel()
	record, err := serviceendpoint.Check(ctx, c.resolver, c.service)
	if err != nil {
		return err
	}
	if record.FleetID != caller.FleetID {
		return errors.New("memory client Fleet mismatch")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(data) > maxPayload {
		return errors.New("memory payload exceeds budget")
	}
	who, err := json.Marshal(caller)
	if err != nil {
		return err
	}
	cli, err := memory.NewClient("juex-memory", client.WithShortConnection(), client.WithConnectTimeout(rpcBudget), client.WithRPCTimeout(rpcBudget))
	if err != nil {
		return err
	}
	if closer, ok := cli.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	request := &memorywire.Call{FleetID: record.FleetID, ServiceID: record.ServiceID, InstanceID: record.InstanceID, Caller: string(who), Payload: string(data)}
	opts := []callopt.Option{callopt.WithHostPort(record.Address)}
	var reply string
	switch method {
	case "Status":
		reply, err = cli.Status(ctx, request, opts...)
	case "Search":
		reply, err = cli.Search(ctx, request, opts...)
	case "Read":
		reply, err = cli.Read(ctx, request, opts...)
	case "Propose":
		reply, err = cli.Propose(ctx, request, opts...)
	case "Result":
		reply, err = cli.Result_(ctx, request, opts...)
	case "Claim":
		reply, err = cli.Claim(ctx, request, opts...)
	case "Decide":
		reply, err = cli.Decide(ctx, request, opts...)
	case "Fail":
		reply, err = cli.Fail(ctx, request, opts...)
	case "Revoke":
		reply, err = cli.Revoke(ctx, request, opts...)
	case "Participation":
		reply, err = cli.Participation(ctx, request, opts...)
	case "Contribute":
		reply, err = cli.Contribute(ctx, request, opts...)
	case "Maintain":
		reply, err = cli.Maintain(ctx, request, opts...)
	case "History":
		reply, err = cli.History(ctx, request, opts...)
	case "Recall":
		reply, err = cli.Recall(ctx, request, opts...)
	case "Admin":
		reply, err = cli.Admin(ctx, request, opts...)
	default:
		return errors.New("unknown memory method")
	}
	if err != nil {
		return err
	}
	if len(reply) > maxPayload {
		return errors.New("memory response exceeds budget")
	}
	return json.Unmarshal([]byte(reply), output)
}

type rpcHandler struct {
	api      API
	identity serviceendpoint.Identity
}

func Register(svr server.Server, identity serviceendpoint.Identity, api API) error {
	return memory.RegisterService(svr, &rpcHandler{api: api, identity: identity})
}
func (h *rpcHandler) decode(request *memorywire.Call, input any) (Caller, error) {
	if request == nil || request.FleetID != h.identity.FleetID || request.ServiceID != h.identity.ServiceID || request.InstanceID != h.identity.InstanceID {
		return Caller{}, errors.New("memory service identity mismatch")
	}
	if len(request.Payload) > maxPayload || len(request.Caller) > 8192 {
		return Caller{}, errors.New("memory call exceeds budget")
	}
	var caller Caller
	if err := json.Unmarshal([]byte(request.Caller), &caller); err != nil {
		return caller, err
	}
	if caller.FleetID != h.identity.FleetID {
		return caller, errors.New("memory caller Fleet mismatch")
	}
	return caller, json.Unmarshal([]byte(request.Payload), input)
}
func encode(value any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) > maxPayload {
		return "", fmt.Errorf("memory response exceeds %d bytes", maxPayload)
	}
	return string(data), nil
}
func (c *Client) Status(ctx context.Context, caller Caller) (out Status, err error) {
	err = c.call(ctx, caller, "Status", struct{}{}, &out)
	return
}
func (h *rpcHandler) Status(ctx context.Context, request *memorywire.Call) (string, error) {
	var q struct{}
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Status(ctx, c))
}
func (c *Client) Search(ctx context.Context, caller Caller, q Query) (out Page, err error) {
	err = c.call(ctx, caller, "Search", q, &out)
	return
}
func (h *rpcHandler) Search(ctx context.Context, request *memorywire.Call) (string, error) {
	var q Query
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Search(ctx, c, q))
}
func (c *Client) Read(ctx context.Context, caller Caller, q ReadRequest) (out Entry, err error) {
	err = c.call(ctx, caller, "Read", q, &out)
	return
}
func (h *rpcHandler) Read(ctx context.Context, request *memorywire.Call) (string, error) {
	var q ReadRequest
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Read(ctx, c, q))
}
func (c *Client) Propose(ctx context.Context, caller Caller, q Proposal) (out Receipt, err error) {
	err = c.call(ctx, caller, "Propose", q, &out)
	return
}
func (h *rpcHandler) Propose(ctx context.Context, request *memorywire.Call) (string, error) {
	var q Proposal
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Propose(ctx, c, q))
}
func (c *Client) Result(ctx context.Context, caller Caller, q string) (out Receipt, err error) {
	err = c.call(ctx, caller, "Result", q, &out)
	return
}
func (h *rpcHandler) Result_(ctx context.Context, request *memorywire.Call) (string, error) {
	var q string
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Result(ctx, c, q))
}
func (c *Client) Claim(ctx context.Context, caller Caller) (out *Assignment, err error) {
	err = c.call(ctx, caller, "Claim", struct{}{}, &out)
	return
}
func (h *rpcHandler) Claim(ctx context.Context, request *memorywire.Call) (string, error) {
	var q struct{}
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Claim(ctx, c))
}
func (c *Client) Decide(ctx context.Context, caller Caller, q Decision) (out Receipt, err error) {
	err = c.call(ctx, caller, "Decide", q, &out)
	return
}
func (h *rpcHandler) Decide(ctx context.Context, request *memorywire.Call) (string, error) {
	var q Decision
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Decide(ctx, c, q))
}
func (c *Client) Fail(ctx context.Context, caller Caller, q Failure) (out Receipt, err error) {
	err = c.call(ctx, caller, "Fail", q, &out)
	return
}
func (h *rpcHandler) Fail(ctx context.Context, request *memorywire.Call) (string, error) {
	var q Failure
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Fail(ctx, c, q))
}
func (c *Client) Revoke(ctx context.Context, caller Caller, q string) (out Settlement, err error) {
	err = c.call(ctx, caller, "Revoke", q, &out)
	return
}
func (h *rpcHandler) Revoke(ctx context.Context, request *memorywire.Call) (string, error) {
	var q string
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Revoke(ctx, c, q))
}
func (c *Client) Participation(ctx context.Context, caller Caller, q Boundary) (out SourceState, err error) {
	err = c.call(ctx, caller, "Participation", q, &out)
	return
}
func (h *rpcHandler) Participation(ctx context.Context, request *memorywire.Call) (string, error) {
	var q Boundary
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Participation(ctx, c, q))
}
func (c *Client) Contribute(ctx context.Context, caller Caller, q SourceBatch) (out SourceState, err error) {
	err = c.call(ctx, caller, "Contribute", q, &out)
	return
}
func (h *rpcHandler) Contribute(ctx context.Context, request *memorywire.Call) (string, error) {
	var q SourceBatch
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Contribute(ctx, c, q))
}
func (c *Client) Maintain(ctx context.Context, caller Caller, q string) (out Receipt, err error) {
	err = c.call(ctx, caller, "Maintain", q, &out)
	return
}
func (h *rpcHandler) Maintain(ctx context.Context, request *memorywire.Call) (string, error) {
	var q string
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Maintain(ctx, c, q))
}
func (c *Client) History(ctx context.Context, caller Caller, q Source) (out []Evidence, err error) {
	err = c.call(ctx, caller, "History", q, &out)
	return
}
func (h *rpcHandler) History(ctx context.Context, request *memorywire.Call) (string, error) {
	var q Source
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.History(ctx, c, q))
}
func (c *Client) Recall(ctx context.Context, caller Caller, q Query) (out Recall, err error) {
	err = c.call(ctx, caller, "Recall", q, &out)
	return
}
func (h *rpcHandler) Recall(ctx context.Context, request *memorywire.Call) (string, error) {
	var q Query
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Recall(ctx, c, q))
}
func (c *Client) Admin(ctx context.Context, caller Caller, q AdminRequest) (out Receipt, err error) {
	err = c.call(ctx, caller, "Admin", q, &out)
	return
}
func (h *rpcHandler) Admin(ctx context.Context, request *memorywire.Call) (string, error) {
	var q AdminRequest
	c, err := h.decode(request, &q)
	if err != nil {
		return "", err
	}
	return encode(h.api.Admin(ctx, c, q))
}

var _ API = (*Client)(nil)
