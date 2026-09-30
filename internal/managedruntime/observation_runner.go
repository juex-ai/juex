package managedruntime

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
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
	observationLoop(ctx, "execution observation delayed", func(ctx context.Context) error {
		source, err := r.observations.ClaimObservation(ctx, holder)
		if err != nil {
			return err
		}
		batch := ObservationBatch{Cursor: source.Cursor, Pending: source.Pending, Discarding: source.Discarding, RetryAfter: 5 * time.Second}
		fresh, err := r.authority.Authorize(ctx, source.Scope.ActorID, source.Scope.TenantID, source.Scope.AgentID, true)
		if errors.Is(err, ErrDenied) || err == nil && !source.Scope.SameAuthority(fresh) {
			batch.Closed = true
			return r.observations.FinishObservation(ctx, source, batch)
		}
		if err != nil {
			return r.observations.FinishObservation(ctx, source, batch)
		}
		operation, err := r.gateway.Operation(ctx, source.Scope, source.EnvironmentID, source.OperationID, source.Cursor)
		if errors.Is(err, execprotocol.ErrDenied) || errors.Is(err, execprotocol.ErrNotFound) && !source.DeliveryPending {
			batch.Closed = true
			return r.observations.FinishObservation(ctx, source, batch)
		}
		if err != nil {
			return r.observations.FinishObservation(ctx, source, batch)
		}
		batch, err = parseObservation(source, operation)
		if err != nil {
			return err
		}
		return r.observations.FinishObservation(ctx, source, batch)
	})
}

func (r toolRunner) deliverObservations(ctx context.Context) {
	holder := rand.Text()
	observationLoop(ctx, "observation wakeup delayed", func(ctx context.Context) error {
		delivery, err := r.observations.ClaimObservationDelivery(ctx, holder)
		if err != nil {
			return err
		}
		fresh, err := r.authority.Authorize(ctx, delivery.Scope.ActorID, delivery.Scope.TenantID, delivery.Scope.AgentID, true)
		if errors.Is(err, ErrDenied) || err == nil && !delivery.Scope.SameAuthority(fresh) {
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
