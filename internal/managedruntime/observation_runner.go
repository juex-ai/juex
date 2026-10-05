package managedruntime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"log/slog"
	"slices"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func observationGrant(environments []execprotocol.Environment, id string, version int64, capability execprotocol.Capability) bool {
	for _, environment := range environments {
		if environment.ID == id && environment.AuthorizationVersion == version && (capability == "" || slices.Contains(environment.Capabilities, capability)) {
			return true
		}
	}
	return false
}

func presence(environment execprotocol.Environment) json.RawMessage {
	encoded, _ := json.Marshal(map[string]any{"online": environment.Online, "availability": environment.Availability})
	return encoded
}

func observationLoop(ctx context.Context, label string, tick func(context.Context) error) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		call, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := tick(call)
		cancel()
		if err != nil && !errors.Is(err, ErrNoWork) && !errors.Is(err, ErrFence) && ctx.Err() == nil {
			slog.Warn(label, "error", err)
		}
	}
}

func (r toolRunner) observe(ctx context.Context) {
	holder := rand.Text()
	defer r.releaseObservationClaims(holder)
	observationLoop(ctx, "execution observation delayed", func(ctx context.Context) error {
		source, err := r.observations.ClaimObservation(ctx, holder)
		if err != nil {
			return err
		}
		batch := ObservationBatch{Cursor: source.Cursor, Pending: source.Pending, Discarding: source.Discarding, Command: source.Command, RetryAfter: 5 * time.Second}
		fresh, err := r.authority.Authorize(ctx, source.Scope.ActorID, source.Scope.TenantID, source.Scope.AgentID, true)
		if err != nil && !errors.Is(err, ErrDenied) {
			return r.observations.FinishObservation(ctx, source, batch)
		}
		publish := err == nil && source.Scope.SameAuthority(fresh) && source.Scope.Capabilities.Allows(agentpolicy.Observations) && fresh.Capabilities.Allows(agentpolicy.Observations)
		operation, err := r.gateway.Operation(ctx, source.Scope, source.EnvironmentID, source.OperationID, source.Cursor)
		if errors.Is(err, execprotocol.ErrDenied) || errors.Is(err, execprotocol.ErrNotFound) && !source.DeliveryPending {
			batch.Closed = true
			return r.observations.FinishObservation(ctx, source, batch)
		}
		if err != nil {
			return r.observations.FinishObservation(ctx, source, batch)
		}
		if !publish {
			batch, err = acknowledgeOnlyObservation(source, operation)
			if err != nil {
				return err
			}
			return r.observations.FinishObservation(ctx, source, batch)
		}
		batch, err = parseObservation(source, operation)
		if err != nil {
			return err
		}
		ready, err := r.captureObservationAttachments(ctx, source, &batch)
		if err != nil {
			return err
		}
		if !ready {
			batch = ObservationBatch{Cursor: source.Cursor, Pending: source.Pending, Discarding: source.Discarding, Command: source.Command, RetryAfter: time.Second}
		}
		return r.observations.FinishObservation(ctx, source, batch)
	})
}

// Output acknowledgment belongs to the already admitted operation. Revoking
// observations must not leak its retained output or publish buffered events.
func acknowledgeOnlyObservation(source ObservationSource, operation ToolOperation) (ObservationBatch, error) {
	snapshot := operation.Snapshot
	if snapshot.NextCursor != source.Cursor+int64(len(snapshot.Output)) || snapshot.OutputBytes < snapshot.NextCursor {
		return ObservationBatch{}, execprotocol.ErrInvalid
	}
	batch := ObservationBatch{Cursor: snapshot.NextCursor, More: snapshot.NextCursor < snapshot.OutputBytes, RetryAfter: 5 * time.Second}
	batch.Closed = snapshot.OutputExpired || execprotocol.State(operation.State).Terminal() && !batch.More
	return batch, nil
}

func (r toolRunner) deliverObservations(ctx context.Context) {
	holder := rand.Text()
	defer r.releaseObservationClaims(holder)
	observationLoop(ctx, "observation wakeup delayed", func(ctx context.Context) error {
		delivery, err := r.observations.ClaimObservationDelivery(ctx, holder)
		if err != nil {
			return err
		}
		fresh, err := r.authority.Authorize(ctx, delivery.Scope.ActorID, delivery.Scope.TenantID, delivery.Scope.AgentID, true)
		if errors.Is(err, ErrDenied) || err == nil && (!delivery.Scope.SameAuthority(fresh) || !fresh.Capabilities.Allows(agentpolicy.Observations)) {
			return r.observations.FinishObservationDelivery(ctx, delivery, false, nil)
		}
		if err != nil {
			return err
		}
		environments, err := r.gateway.Environments(ctx, delivery.Scope)
		if err != nil {
			return err
		}
		valid := observationGrant(environments, delivery.Observation.EnvironmentID, delivery.AuthorizationVersion, delivery.Capability)
		var baseline json.RawMessage
		if valid && delivery.Kind == "environment.presence" {
			for _, environment := range environments {
				if environment.ID == delivery.Observation.EnvironmentID {
					baseline = presence(environment)
					break
				}
			}
		}
		return r.observations.FinishObservationDelivery(ctx, delivery, valid, baseline)
	})
}

func (r toolRunner) releaseObservationClaims(holder string) {
	// A cancelled claim can have committed even when its response was lost.
	// Release by worker identity after its loop ends, including that case.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.observations.ReleaseObservationClaims(ctx, holder); err != nil {
		slog.Warn("observation shutdown release delayed", "error", err)
	}
}

func (r toolRunner) acknowledgeObservations(ctx context.Context) {
	observationLoop(ctx, "observation acknowledgment delayed", func(ctx context.Context) error {
		acks, err := r.observations.ObservationAcks(ctx, 100)
		if err != nil {
			return err
		}
		for _, ack := range acks {
			if err := r.gateway.AcknowledgeOutput(ctx, ack.Scope, ack.EnvironmentID, ack.OperationID, ack.Cursor); err != nil {
				if errors.Is(err, execprotocol.ErrDenied) || errors.Is(err, execprotocol.ErrNotFound) {
					continue
				}
				return err
			}
			if err := r.observations.ConfirmObservationAck(ctx, ack); err != nil {
				return err
			}
		}
		return nil
	})
}
