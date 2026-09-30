package execution

import (
	"context"
	"crypto/rand"
	"errors"
	"reflect"
	"slices"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// Exchange is one correlated request/reply on an outbound device connection.
// Only the broker writes the connection, so cancellation and grant updates
// share the same ordering as operation dispatch.
type Exchange func(context.Context, execprotocol.Envelope) (execprotocol.Envelope, error)

func callDevice(ctx context.Context, exchange Exchange, request execprotocol.Envelope) (execprotocol.Envelope, error) {
	request.Version, request.ID = execprotocol.Version, rand.Text()
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reply, err := exchange(callCtx, request)
	if err != nil {
		return reply, err
	}
	if reply.Version != execprotocol.Version {
		return reply, execprotocol.ErrVersion
	}
	if reply.ID != request.ID || reply.Type != "result" {
		return reply, execprotocol.ErrInvalid
	}
	return reply, execprotocol.FromErrorCode(reply.Error)
}

func (s *Service) Connected(ctx context.Context, device Device, exchange Exchange) error {
	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.Store.Touch(stop, device.ID, device.ConnectionEpoch, false)
	}()
	var grants map[string][]execprotocol.Capability
	var status string
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		fresh, err := s.Store.Device(ctx, device.ID)
		if err != nil {
			return err
		}
		if fresh.ConnectionEpoch != device.ConnectionEpoch {
			return execprotocol.ErrConflict
		}
		current, err := s.EffectiveGrants(ctx, fresh)
		if err != nil {
			current = map[string][]execprotocol.Capability{}
		}
		if grants == nil || status != fresh.Status || !reflect.DeepEqual(grants, current) {
			if _, err := callDevice(ctx, exchange, execprotocol.Envelope{Type: "grants", Grants: current, Revoked: fresh.Status != "active"}); err != nil {
				return err
			}
			grants = current
			status = fresh.Status
		}
		if err := s.Store.Touch(ctx, device.ID, device.ConnectionEpoch, true); err != nil {
			return err
		}
		operations, err := s.Store.Pending(ctx, device.ID, 32)
		if err != nil {
			return err
		}
		for _, operation := range operations {
			if err := s.reconcileDeviceOperation(ctx, fresh, grants, operation, exchange); err != nil {
				return err
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (s *Service) reconcileDeviceOperation(ctx context.Context, device Device, grants map[string][]execprotocol.Capability, operation Operation, exchange Exchange) error {
	terminal := execprotocol.State(operation.State).Terminal()
	if !terminal {
		fresh, err := s.Authority.Agent(ctx, operation.Scope.ActorID, operation.Scope.TenantID, operation.Scope.AgentID, true)
		if err != nil || !sameGeneration(fresh, operation.Scope) || !permits(device, fresh, operation.Request.Kind) || !slices.Contains(grants[operation.Scope.AgentID], execprotocol.RequiredCapability(operation.Request.Kind)) {
			if err := s.Store.CancelOperation(ctx, device.ID, operation.ID); err != nil {
				return err
			}
			operation.CancelRequested = true
			if operation.State == "waiting" {
				return nil
			}
		}
	}
	if operation.State == "waiting" && !time.Now().Before(operation.WaitUntil) {
		return s.Store.Settle(ctx, device.ID, device.ConnectionEpoch, operation.ID, execprotocol.Failed, "environment wait expired before dispatch")
	}
	if terminal && operation.ResultCursor == operation.Snapshot.OutputBytes {
		_, err := callDevice(ctx, exchange, execprotocol.Envelope{Type: "ack", AgentID: operation.Scope.AgentID, OperationID: operation.ID, Cursor: operation.ResultCursor})
		if err != nil {
			return err
		}
		return s.Store.Acknowledge(ctx, device.ID, device.ConnectionEpoch, operation.ID)
	}
	reply, err := callDevice(ctx, exchange, execprotocol.Envelope{Type: "query", AgentID: operation.Scope.AgentID, OperationID: operation.ID, Cursor: operation.ResultCursor, Limit: 64 << 10})
	if errors.Is(err, execprotocol.ErrNotFound) {
		if operation.CancelRequested {
			return s.Store.Settle(ctx, device.ID, device.ConnectionEpoch, operation.ID, execprotocol.Cancelled, "cancelled before device acceptance")
		}
		if terminal {
			return execprotocol.ErrConflict
		}
		if operation.State == "waiting" {
			operation, err = s.Store.Dispatch(ctx, device.ID, device.ConnectionEpoch, operation.ID)
			if err != nil {
				if errors.Is(err, execprotocol.ErrDenied) {
					return nil
				}
				return err
			}
		}
		// A dispatch lost before acknowledgment is resent only with its original
		// identity, on the same durable device journal, after a negative query.
		reply, err = callDevice(ctx, exchange, execprotocol.Envelope{Type: "submit", Request: &operation.Request})
		if err != nil {
			if errors.Is(err, execprotocol.ErrQuota) || errors.Is(err, execprotocol.ErrInvalid) || errors.Is(err, execprotocol.ErrDenied) {
				return s.Store.Settle(ctx, device.ID, device.ConnectionEpoch, operation.ID, execprotocol.Failed, execprotocol.ErrorCode(err))
			}
			return err
		}
	}
	if err != nil {
		return err
	}
	if reply.Snapshot == nil {
		return execprotocol.ErrInvalid
	}
	if err := s.Store.Observe(ctx, device.ID, device.ConnectionEpoch, *reply.Snapshot); err != nil {
		return err
	}
	if operation.CancelRequested && !reply.Snapshot.State.Terminal() {
		_, err := callDevice(ctx, exchange, execprotocol.Envelope{Type: "cancel", AgentID: operation.Scope.AgentID, OperationID: operation.ID})
		return err
	}
	return nil
}

// Reconcile also visits disconnected devices: queued work expires without an
// activation, and lifecycle changes persist cancellation for the next contact.
func (s *Service) Reconcile(ctx context.Context) error {
	if err := s.Store.ExpireWaiting(ctx); err != nil {
		return err
	}
	operations, err := s.Store.Unsettled(ctx, 256)
	if err != nil {
		return err
	}
	for _, operation := range operations {
		fresh, err := s.Authority.Agent(ctx, operation.Scope.ActorID, operation.Scope.TenantID, operation.Scope.AgentID, true)
		if err != nil && !errors.Is(err, execprotocol.ErrDenied) {
			continue
		}
		device, deviceErr := s.Store.Device(ctx, operation.EnvironmentID)
		if deviceErr != nil {
			return deviceErr
		}
		if err != nil || !sameGeneration(fresh, operation.Scope) || !permits(device, fresh, operation.Request.Kind) {
			if err := s.Store.CancelOperation(ctx, operation.EnvironmentID, operation.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
