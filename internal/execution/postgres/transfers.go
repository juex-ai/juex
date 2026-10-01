package postgres

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

const transferColumns = `id,scope,request,state,COALESCE(artifact_id::text,''),cancel_requested,error,wait_until,created_at`

func scanTransfer(row pgx.Row) (execution.Transfer, error) {
	var transfer execution.Transfer
	err := row.Scan(&transfer.ID, &transfer.Scope, &transfer.Request, &transfer.State, &transfer.ArtifactID, &transfer.CancelRequested, &transfer.Error, &transfer.WaitUntil, &transfer.CreatedAt)
	return transfer, classify(err)
}

// Environment-first ordering also matches grants, operation admission and
// hosted reclamation. Two transfers in opposite directions cannot deadlock.
func lockTransferDevices(ctx context.Context, tx pgx.Tx, request execution.TransferRequest) (map[string]execution.Device, error) {
	var ids []string
	for _, location := range []*execution.FileLocation{request.Source, request.Target} {
		if location != nil {
			ids = append(ids, location.EnvironmentID)
		}
	}
	slices.Sort(ids)
	devices := map[string]execution.Device{}
	for _, id := range slices.Compact(ids) {
		device, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return nil, err
		}
		devices[id] = device
	}
	return devices, nil
}

func transferDevicesAllowed(devices map[string]execution.Device, scope execution.Scope, request execution.TransferRequest) bool {
	for _, location := range []*execution.FileLocation{request.Source, request.Target} {
		if location != nil && !location.Permits(devices[location.EnvironmentID], scope) {
			return false
		}
	}
	return true
}

func (s *Store) AdmitTransfer(ctx context.Context, scope execution.Scope, request execution.TransferRequest, artifact *execution.Artifact) (execution.Transfer, error) {
	if err := request.Validate(); err != nil {
		return execution.Transfer{}, err
	}
	identity := scope
	identity.OwnerEmail, identity.TenantName = "", ""
	hash, err := execution.CanonicalHash(struct {
		Scope   execution.Scope
		Request execution.TransferRequest
	}{identity, request})
	if err != nil {
		return execution.Transfer{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Transfer{}, err
	}
	defer rollback(tx)
	if err := purgeGate(ctx, tx, scope.FleetID, scope.AgentID); err != nil {
		return execution.Transfer{}, err
	}
	devices, err := lockTransferDevices(ctx, tx, request)
	if err != nil {
		return execution.Transfer{}, err
	}
	if !transferDevicesAllowed(devices, scope, request) {
		return execution.Transfer{}, execprotocol.ErrDenied
	}
	id := execution.TransferID(scope.AgentID, request.RequestID)
	if err := lockTransfer(ctx, tx, id); err != nil {
		return execution.Transfer{}, err
	}
	var storedHash string
	err = tx.QueryRow(ctx, `SELECT request_hash FROM execution.transfers WHERE agent_id=$1 AND request_id=$2`, scope.AgentID, request.RequestID).Scan(&storedHash)
	if err == nil {
		transfer, err := scanTransfer(tx.QueryRow(ctx, `SELECT `+transferColumns+` FROM execution.transfers WHERE agent_id=$1 AND request_id=$2`, scope.AgentID, request.RequestID))
		if err != nil {
			return transfer, err
		}
		if !transfer.Scope.SameAuthority(scope) {
			return transfer, execprotocol.ErrDenied
		}
		if storedHash != hash {
			return transfer, execprotocol.ErrConflict
		}
		return transfer, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return execution.Transfer{}, err
	}
	cancelled, err := cancelledBeforeAdmission(ctx, tx, scope, "transfer", "", id)
	if err != nil {
		return execution.Transfer{}, err
	}
	if cancelled {
		transfer, err := scanTransfer(tx.QueryRow(ctx, `INSERT INTO execution.transfers(id,agent_id,request_id,scope,request,request_hash,state,cancel_requested) VALUES($1,$2,$3,$4,$5,$6,'cancelled',true) RETURNING `+transferColumns, id, scope.AgentID, request.RequestID, scope, request, hash))
		if err != nil {
			return transfer, err
		}
		return transfer, tx.Commit(ctx)
	}
	var artifactID any
	if request.ArtifactID != "" {
		current, err := scanArtifact(tx.QueryRow(ctx, `SELECT `+artifactColumns+` FROM execution.artifacts WHERE id=$1 AND NOT purge_blocked FOR SHARE`, request.ArtifactID))
		if err != nil {
			return execution.Transfer{}, err
		}
		if artifact == nil || artifact.ID != current.ID || current.State != "ready" || !current.ReadableBy(scope) || current.Request.Manifest != artifact.Request.Manifest {
			return execution.Transfer{}, execprotocol.ErrDenied
		}
		artifactID = current.ID
	}
	transfer, err := scanTransfer(tx.QueryRow(ctx, `INSERT INTO execution.transfers(id,agent_id,request_id,scope,request,request_hash,artifact_id) VALUES($1,$2,$3,$4,$5,$6,$7) RETURNING `+transferColumns, execution.TransferID(scope.AgentID, request.RequestID), scope.AgentID, request.RequestID, scope, request, hash, artifactID))
	if err != nil {
		return transfer, err
	}
	source := request.Source != nil
	location := request.Source
	var manifest *execprotocol.FileManifest
	if !source {
		location, manifest = request.Target, &artifact.Request.Manifest
	}
	if _, err := s.enqueue(ctx, tx, devices[location.EnvironmentID], scope, transfer.FileOperation(source, manifest), time.Until(transfer.WaitUntil), false); err != nil {
		return transfer, err
	}
	return transfer, tx.Commit(ctx)
}

func (s *Store) Transfer(ctx context.Context, id string) (execution.Transfer, error) {
	transfer, err := scanTransfer(s.pool.QueryRow(ctx, `SELECT `+transferColumns+` FROM execution.transfers WHERE id=$1`, id))
	if errors.Is(err, execprotocol.ErrDenied) {
		err = execprotocol.ErrNotFound
	}
	return transfer, err
}

func (s *Store) ListTransfers(ctx context.Context, scope execution.Scope, after string, limit int) ([]execution.Transfer, error) {
	if limit < 1 || limit > 100 {
		return nil, execprotocol.ErrInvalid
	}
	if after == "" {
		after = uuid.Nil.String()
	}
	rows, err := s.pool.Query(ctx, `SELECT `+transferColumns+` FROM execution.transfers WHERE agent_id=$1 AND scope->>'tenant_id'=$2 AND scope->>'user_id'=$3 AND scope->>'fleet_id'=$4 AND id>$5 ORDER BY id LIMIT $6`, scope.AgentID, scope.TenantID, scope.UserID, scope.FleetID, after, limit)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	result := []execution.Transfer{}
	for rows.Next() {
		value, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *Store) ActiveTransfers(ctx context.Context, limit int) ([]execution.Transfer, error) {
	if limit < 1 || limit > 256 {
		return nil, execprotocol.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	rows, err := tx.Query(ctx, `UPDATE execution.transfers SET updated_at=clock_timestamp() WHERE id IN (SELECT id FROM execution.transfers WHERE state='accepted' ORDER BY updated_at,id LIMIT $1) RETURNING `+transferColumns, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []execution.Transfer{}
	for rows.Next() {
		transfer, err := scanTransfer(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, transfer)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) AttachTransferArtifact(ctx context.Context, prior execution.Transfer, artifact execution.Artifact) (execution.Transfer, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Transfer{}, err
	}
	defer rollback(tx)
	devices, err := lockTransferDevices(ctx, tx, prior.Request)
	if err != nil {
		return execution.Transfer{}, err
	}
	transfer, err := scanTransfer(tx.QueryRow(ctx, `SELECT `+transferColumns+` FROM execution.transfers WHERE id=$1 FOR UPDATE`, prior.ID))
	if err != nil {
		return transfer, err
	}
	if transfer.ArtifactID != "" {
		if transfer.ArtifactID != artifact.ID {
			return transfer, execprotocol.ErrConflict
		}
		return transfer, tx.Commit(ctx)
	}
	if transfer.State.Terminal() || transfer.CancelRequested || !transfer.Scope.SameAuthority(prior.Scope) || !transferDevicesAllowed(devices, transfer.Scope, transfer.Request) {
		return transfer, execprotocol.ErrDenied
	}
	current, err := scanArtifact(tx.QueryRow(ctx, `SELECT `+artifactColumns+` FROM execution.artifacts WHERE id=$1 AND NOT purge_blocked FOR SHARE`, artifact.ID))
	if err != nil {
		return transfer, err
	}
	if current.State != "ready" || !current.Scope.SameAuthority(transfer.Scope) || current.Request.Manifest != artifact.Request.Manifest || current.Request.Source == nil || current.Request.Source.OperationID != transfer.FileOperation(true, nil).ID {
		return transfer, execprotocol.ErrDenied
	}
	if transfer.Request.Target != nil {
		if !time.Now().Before(transfer.WaitUntil) {
			return transfer, execprotocol.ErrConflict
		}
		if _, err := s.enqueue(ctx, tx, devices[transfer.Request.Target.EnvironmentID], transfer.Scope, transfer.FileOperation(false, &current.Request.Manifest), time.Until(transfer.WaitUntil), false); err != nil {
			return transfer, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE execution.transfers SET artifact_id=$2,updated_at=clock_timestamp() WHERE id=$1`, transfer.ID, artifact.ID); err != nil {
		return transfer, err
	}
	transfer.ArtifactID = artifact.ID
	return transfer, tx.Commit(ctx)
}

func (s *Store) CancelTransfer(ctx context.Context, id string) error {
	prior, err := s.Transfer(ctx, id)
	if err != nil {
		return err
	}
	return s.cancelTransfer(ctx, prior, nil)
}

func (s *Store) cancelTransfer(ctx context.Context, prior execution.Transfer, scope *execution.Scope) error {
	id := prior.ID
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := lockTransferDevices(ctx, tx, prior.Request); err != nil {
		return err
	}
	if err := lockTransfer(ctx, tx, id); err != nil {
		return err
	}
	transfer, err := scanTransfer(tx.QueryRow(ctx, `SELECT `+transferColumns+` FROM execution.transfers WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return err
	}
	if scope != nil {
		if !sameCancellationOwner(transfer.Scope, *scope) {
			return execprotocol.ErrDenied
		}
		if err := recordCancellation(ctx, tx, *scope, "transfer", "", id); err != nil {
			return err
		}
	}
	if transfer.State.Terminal() {
		return tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `UPDATE execution.transfers SET cancel_requested=true,updated_at=clock_timestamp() WHERE id=$1`, id); err != nil {
		return err
	}
	for _, source := range []bool{true, false} {
		location := transfer.Request.Target
		if source {
			location = transfer.Request.Source
		}
		if location == nil || !source && transfer.ArtifactID == "" {
			continue
		}
		request := transfer.FileOperation(source, nil)
		if err := cancelOperation(ctx, tx, location.EnvironmentID, request.ID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) FinishTransfer(ctx context.Context, id string, state execprotocol.State, message string) error {
	if !state.Terminal() || len(message) > 4096 {
		return execprotocol.ErrInvalid
	}
	_, err := s.exec(ctx, `UPDATE execution.transfers SET state=$2,error=$3,updated_at=clock_timestamp() WHERE id=$1 AND state='accepted'`, id, state, message)
	return err
}

func (s *Store) SetTransferNotice(ctx context.Context, id, message string) error {
	if len(message) > 4096 {
		return execprotocol.ErrInvalid
	}
	_, err := s.exec(ctx, `UPDATE execution.transfers SET error=$2,updated_at=clock_timestamp() WHERE id=$1 AND state='accepted' AND error!=$2`, id, message)
	return err
}

func (s *Store) ExtendTransfer(ctx context.Context, id string, wait time.Duration) error {
	prior, err := s.Transfer(ctx, id)
	if err != nil {
		return err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	devices, err := lockTransferDevices(ctx, tx, prior.Request)
	if err != nil {
		return err
	}
	if !transferDevicesAllowed(devices, prior.Scope, prior.Request) {
		return execprotocol.ErrDenied
	}
	result, err := tx.Exec(ctx, `UPDATE execution.transfers SET wait_until=clock_timestamp()+$2*interval '1 millisecond',updated_at=clock_timestamp() WHERE id=$1 AND state='accepted' AND NOT cancel_requested`, id, wait.Milliseconds())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return execprotocol.ErrConflict
	}
	for _, source := range []bool{true, false} {
		location := prior.Request.Target
		if source {
			location = prior.Request.Source
		}
		if location == nil {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE execution.operations SET wait_until=clock_timestamp()+$3*interval '1 millisecond',updated_at=clock_timestamp() WHERE environment_id=$1 AND id=$2 AND state='waiting' AND NOT cancel_requested`, location.EnvironmentID, prior.FileOperation(source, nil).ID, wait.Milliseconds()); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

var _ execution.TransferRepository = (*Store)(nil)
