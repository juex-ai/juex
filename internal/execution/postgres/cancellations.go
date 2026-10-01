package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func sameCancellationOwner(a, b execution.Scope) bool {
	return a.TenantID == b.TenantID && a.UserID == b.UserID && a.FleetID == b.FleetID && a.AgentID == b.AgentID
}

func recordCancellation(ctx context.Context, tx pgx.Tx, scope execution.Scope, kind, environment, id string) error {
	_, err := tx.Exec(ctx, `INSERT INTO execution.cancellations(tenant_id,user_id,fleet_id,agent_id,kind,environment_id,request_id,actor_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, scope.TenantID, scope.UserID, scope.FleetID, scope.AgentID, kind, environment, id, scope.ActorID)
	return err
}

func cancelledBeforeAdmission(ctx context.Context, tx pgx.Tx, scope execution.Scope, kind, environment, id string) (bool, error) {
	var cancelled bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution.cancellations WHERE tenant_id=$1 AND user_id=$2 AND fleet_id=$3 AND agent_id=$4 AND kind=$5 AND environment_id=$6 AND request_id=$7)`, scope.TenantID, scope.UserID, scope.FleetID, scope.AgentID, kind, environment, id).Scan(&cancelled)
	return cancelled, err
}

func (s *Store) CancelPreparedOperation(ctx context.Context, scope execution.Scope, environment, id string) (execprotocol.State, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollback(tx)
	device, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE id=$1 FOR UPDATE`, environment))
	if err != nil {
		return "", err
	}
	if device.TenantID != scope.TenantID || device.UserID != scope.UserID || device.FleetID != scope.FleetID {
		return "", execprotocol.ErrDenied
	}
	operation, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM execution.operations WHERE environment_id=$1 AND id=$2`, environment, id))
	if err != nil && !errors.Is(err, execprotocol.ErrDenied) {
		return "", err
	}
	state := execprotocol.Cancelled
	if err == nil {
		state = execprotocol.State(operation.State)
		if state == "waiting" {
			state = execprotocol.Cancelled
		}
		if !sameCancellationOwner(operation.Scope, scope) {
			return "", execprotocol.ErrDenied
		}
		if err := cancelOperation(ctx, tx, environment, id); err != nil {
			return "", err
		}
	}
	if err := recordCancellation(ctx, tx, scope, "operation", environment, id); err != nil {
		return "", err
	}
	return state, tx.Commit(ctx)
}

func lockTransfer(ctx context.Context, tx pgx.Tx, id string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "transfer/"+id)
	return err
}

func (s *Store) CancelPreparedTransfer(ctx context.Context, scope execution.Scope, id string) (execprotocol.State, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer rollback(tx)
	if err := lockTransfer(ctx, tx, id); err != nil {
		return "", err
	}
	prior, err := scanTransfer(tx.QueryRow(ctx, `SELECT `+transferColumns+` FROM execution.transfers WHERE id=$1`, id))
	if errors.Is(err, execprotocol.ErrDenied) {
		if err := recordCancellation(ctx, tx, scope, "transfer", "", id); err != nil {
			return "", err
		}
		return execprotocol.Cancelled, tx.Commit(ctx)
	}
	if err != nil {
		return "", err
	}
	if !sameCancellationOwner(prior.Scope, scope) {
		return "", execprotocol.ErrDenied
	}
	// Admission takes environment locks before the transfer lock. Release the
	// lookup lock before following that order for an existing transfer.
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if err := s.cancelTransfer(ctx, prior, &scope); err != nil {
		return "", err
	}
	current, err := s.Transfer(ctx, id)
	return current.State, err
}
