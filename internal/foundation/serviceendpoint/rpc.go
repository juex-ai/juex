package serviceendpoint

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/cloudwego/kitex/client"
	"github.com/cloudwego/kitex/client/callopt"
	"github.com/cloudwego/kitex/server"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint/wire/control"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint/wire/control/servicecontrol"
)

const CallTimeout = 2 * time.Second

func wireIdentity(id Identity) *control.Identity {
	return &control.Identity{FleetID: id.FleetID, ServiceID: id.ServiceID, InstanceID: id.InstanceID}
}

// Probe validates identity in the RPC request and response, including on a new connection.
func Probe(ctx context.Context, record Record) error {
	actual, err := Inspect(ctx, record)
	if err != nil {
		return err
	}
	if actual != record.Identity {
		return errors.New("service identity mismatch")
	}
	return nil
}

// Inspect permits an empty expected instance only when discovering a configured external service.
func Inspect(ctx context.Context, record Record) (Identity, error) {
	var result control.ServiceControlInspectResult
	err := controlCall(ctx, record, "Inspect", &control.ServiceControlInspectArgs{Expected: wireIdentity(record.Identity)}, &result)
	if err != nil {
		return Identity{}, err
	}
	if result.Success == nil {
		return Identity{}, errors.New("empty service identity")
	}
	actual := Identity{FleetID: result.Success.FleetID, ServiceID: result.Success.ServiceID, InstanceID: result.Success.InstanceID}
	if actual.FleetID != record.FleetID || actual.ServiceID != record.ServiceID || actual.InstanceID == "" || (record.InstanceID != "" && actual.InstanceID != record.InstanceID) {
		return Identity{}, errors.New("service identity mismatch")
	}
	return actual, nil
}
func Stop(ctx context.Context, record Record) error {
	if err := record.Validate(); err != nil {
		return err
	}
	return controlCall(ctx, record, "Stop", &control.ServiceControlStopArgs{Expected: wireIdentity(record.Identity)}, &control.ServiceControlStopResult{})
}
func controlCall(ctx context.Context, record Record, method string, args, result any) error {
	if err := ValidateAddress(record.Network, record.Address); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	cli, err := client.NewClient(servicecontrol.NewServiceInfoForClient(), client.WithDestService("juex-service-control"), client.WithShortConnection(), client.WithConnectTimeout(CallTimeout), client.WithRPCTimeout(CallTimeout))
	if err != nil {
		return err
	}
	if closer, ok := cli.(io.Closer); ok {
		defer func() { _ = closer.Close() }()
	}
	ctx = client.NewCtxWithCallOptions(ctx, []callopt.Option{callopt.WithHostPort(record.Address)})
	return cli.Call(ctx, method, args, result)
}

// Check resolves anew for every bounded call. Construction is offline and failures
// are not retried, so future business mutations cannot be duplicated implicitly.
func Check(ctx context.Context, resolver Resolver, service string) (Record, error) {
	record, err := resolver.Resolve(ctx, service)
	if err != nil {
		return Record{}, fmt.Errorf("service %s unavailable: %w", service, err)
	}
	if err := Probe(ctx, record); err != nil {
		return Record{}, fmt.Errorf("service %s unavailable: %w", service, err)
	}
	return record, nil
}

type controlHandler struct {
	identity Identity
	stop     func()
	once     sync.Once
}

func (h *controlHandler) Inspect(_ context.Context, expected *control.Identity) (*control.Identity, error) {
	if err := h.check(expected, false); err != nil {
		return nil, err
	}
	return wireIdentity(h.identity), nil
}
func (h *controlHandler) Stop(_ context.Context, expected *control.Identity) error {
	if err := h.check(expected, true); err != nil {
		return err
	}
	h.once.Do(h.stop)
	return nil
}
func (h *controlHandler) check(expected *control.Identity, exact bool) error {
	if expected == nil || expected.FleetID != h.identity.FleetID || expected.ServiceID != h.identity.ServiceID || ((exact || expected.InstanceID != "") && expected.InstanceID != h.identity.InstanceID) {
		return errors.New("service identity mismatch")
	}
	return nil
}

// ControlServer owns only identity/readiness and graceful shutdown. Business
// services register their own typed RPC on the returned server before Run.
func ControlServer(listener net.Listener, identity Identity, stop func()) server.Server {
	return servicecontrol.NewServer(&controlHandler{identity: identity, stop: stop}, server.WithListener(listener), server.WithExitWaitTime(time.Second), server.WithExitSignal(func() <-chan error { return make(chan error) }))
}
