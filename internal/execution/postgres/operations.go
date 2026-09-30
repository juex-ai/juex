package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

const operationColumns = `id,environment_id,scope,request,state,wait_until,cancel_requested,snapshot,octet_length(output),acknowledged,created_at`

func scanOperation(row pgx.Row) (execution.Operation, error) {
	var operation execution.Operation
	err := row.Scan(&operation.ID, &operation.EnvironmentID, &operation.Scope, &operation.Request, &operation.State, &operation.WaitUntil, &operation.CancelRequested, &operation.Snapshot, &operation.ResultCursor, &operation.Acknowledged, &operation.CreatedAt)
	return operation, classify(err)
}

func (s *Store) Enqueue(ctx context.Context, device execution.Device, scope execution.Scope, request execprotocol.Request, wait time.Duration, hold bool) (execution.Operation, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Operation{}, err
	}
	defer rollback(tx)
	operation, err := s.enqueue(ctx, tx, device, scope, request, wait, hold)
	if err != nil {
		return operation, err
	}
	return operation, tx.Commit(ctx)
}

func (s *Store) enqueue(ctx context.Context, tx pgx.Tx, device execution.Device, scope execution.Scope, request execprotocol.Request, wait time.Duration, hold bool) (execution.Operation, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return execution.Operation{}, err
	}
	owner, err := json.Marshal(scope)
	if err != nil {
		return execution.Operation{}, err
	}
	identity := scope
	identity.OwnerEmail, identity.TenantName = "", ""
	hash, err := execution.CanonicalHash(struct {
		Scope   execution.Scope
		Request execprotocol.Request
		Hold    bool
	}{identity, request, hold})
	if err != nil {
		return execution.Operation{}, err
	}
	snapshot, err := json.Marshal(execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: device.ID, ID: request.ID, AgentID: request.AgentID, Kind: request.Kind, State: execprotocol.Accepted})
	if err != nil {
		return execution.Operation{}, err
	}
	// Lock the environment before every operation mutation, including revocation.
	current, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE id=$1 FOR UPDATE`, device.ID))
	if err != nil {
		return execution.Operation{}, err
	}
	if current.Status != "active" || current.Version != device.Version {
		return execution.Operation{}, execprotocol.ErrDenied
	}
	var storedHash string
	err = tx.QueryRow(ctx, `SELECT request_hash FROM execution.operations WHERE environment_id=$1 AND id=$2`, device.ID, request.ID).Scan(&storedHash)
	if err == nil {
		if storedHash != hash {
			return execution.Operation{}, execprotocol.ErrConflict
		}
		operation, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM execution.operations WHERE environment_id=$1 AND id=$2`, device.ID, request.ID))
		if err != nil {
			return operation, err
		}
		return operation, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return execution.Operation{}, err
	}
	cancelled, err := cancelledBeforeAdmission(ctx, tx, scope, "operation", device.ID, request.ID)
	if err != nil {
		return execution.Operation{}, err
	}
	if cancelled {
		snapshot, _ = json.Marshal(execprotocol.Snapshot{Version: execprotocol.Version, EnvironmentID: device.ID, ID: request.ID, AgentID: request.AgentID, Kind: request.Kind, State: execprotocol.Cancelled})
		operation, err := scanOperation(tx.QueryRow(ctx, `INSERT INTO execution.operations(environment_id,id,scope,request,request_hash,wait_until,snapshot,state,cancel_requested,acknowledged) VALUES($1,$2,$3,$4,$5,clock_timestamp(),$6,'cancelled',true,true) RETURNING `+operationColumns, device.ID, request.ID, owner, encoded, hash, snapshot))
		return operation, err
	}
	var reserved int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(greatest(octet_length(output),CASE WHEN state IN ('waiting','dispatched','accepted','running') THEN 8388608 ELSE 0 END)),0)::bigint FROM execution.operations WHERE environment_id=$1`, device.ID).Scan(&reserved); err != nil {
		return execution.Operation{}, err
	}
	if reserved+8<<20 > 512<<20 {
		return execution.Operation{}, execprotocol.ErrQuota
	}
	if _, err := tx.Exec(ctx, `INSERT INTO execution.operations(environment_id,id,scope,request,request_hash,wait_until,snapshot,output_hold) VALUES($1,$2,$3,$4,$5,clock_timestamp()+$6*interval '1 millisecond',$7,$8) ON CONFLICT(environment_id,id) DO NOTHING`, device.ID, request.ID, owner, encoded, hash, wait.Milliseconds(), snapshot, hold); err != nil {
		return execution.Operation{}, classify(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO execution.audit(tenant_id,owner_id,actor_id,environment_id,agent_id,operation_id,action,version) VALUES($1,$2,$3,$4,$5,$6,'operation.accepted',1)`, scope.TenantID, scope.UserID, scope.ActorID, device.ID, scope.AgentID, request.ID); err != nil {
		return execution.Operation{}, err
	}
	operation, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM execution.operations WHERE environment_id=$1 AND id=$2 AND request_hash=$3`, device.ID, request.ID, hash))
	if err != nil {
		if errors.Is(err, execprotocol.ErrDenied) {
			err = execprotocol.ErrConflict
		}
		return operation, err
	}
	return operation, nil
}

func (s *Store) Operation(ctx context.Context, environment, id string, cursor int64, limit int) (execution.Operation, error) {
	if cursor < 0 || limit < 1 || limit > 256<<10 {
		return execution.Operation{}, execprotocol.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Operation{}, err
	}
	defer rollback(tx)
	operation, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM execution.operations WHERE environment_id=$1 AND id=$2 FOR SHARE`, environment, id))
	if err != nil {
		if errors.Is(err, execprotocol.ErrDenied) {
			err = execprotocol.ErrNotFound
		}
		return operation, err
	}
	if cursor > operation.ResultCursor {
		if !operation.Snapshot.OutputExpired {
			return operation, execprotocol.ErrInvalid
		}
		operation.Snapshot.NextCursor = cursor
		return operation, tx.Commit(ctx)
	}
	if err := tx.QueryRow(ctx, `SELECT substring(output FROM $3::integer FOR $4::integer) FROM execution.operations WHERE environment_id=$1 AND id=$2`, environment, id, cursor+1, limit).Scan(&operation.Snapshot.Output); err != nil {
		return operation, err
	}
	operation.Snapshot.NextCursor = cursor + int64(len(operation.Snapshot.Output))
	return operation, tx.Commit(ctx)
}

func (s *Store) Pending(ctx context.Context, environment string, limit int) ([]execution.Operation, error) {
	if limit < 1 || limit > 1000 {
		return nil, execprotocol.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT `+operationColumns+` FROM execution.operations WHERE environment_id=$1 AND NOT acknowledged ORDER BY updated_at,created_at,id LIMIT $2`, environment, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	operations := []execution.Operation{}
	for rows.Next() {
		operation, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	return operations, rows.Err()
}

func fence(ctx context.Context, tx pgx.Tx, environment string, epoch int64) error {
	var current int64
	if err := tx.QueryRow(ctx, `SELECT connection_epoch FROM execution.environments WHERE id=$1 FOR UPDATE`, environment).Scan(&current); err != nil {
		return classify(err)
	}
	if current != epoch {
		return execprotocol.ErrConflict
	}
	return nil
}

func (s *Store) Dispatch(ctx context.Context, environment string, epoch int64, id string) (execution.Operation, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Operation{}, err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, environment, epoch); err != nil {
		return execution.Operation{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE execution.operations SET state='dispatched',updated_at=clock_timestamp() WHERE environment_id=$1 AND id=$2 AND state='waiting' AND NOT cancel_requested AND wait_until>clock_timestamp()`, environment, id); err != nil {
		return execution.Operation{}, err
	}
	operation, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM execution.operations WHERE environment_id=$1 AND id=$2`, environment, id))
	if err != nil {
		return operation, err
	}
	if operation.State != "dispatched" || operation.CancelRequested {
		return operation, execprotocol.ErrDenied
	}
	device, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE id=$1`, environment))
	if err != nil {
		return operation, err
	}
	if device.Status != "active" || !slices.Contains(device.Grants[operation.Scope.AgentID], execprotocol.RequiredCapability(operation.Request.Kind)) {
		return operation, execprotocol.ErrDenied
	}
	return operation, tx.Commit(ctx)
}

func (s *Store) CancelOperation(ctx context.Context, environment, id string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var locked string
	if err := tx.QueryRow(ctx, `SELECT id FROM execution.environments WHERE id=$1 FOR UPDATE`, environment).Scan(&locked); err != nil {
		return classify(err)
	}
	if err := cancelOperation(ctx, tx, environment, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func cancelOperation(ctx context.Context, tx pgx.Tx, environment, id string) error {
	result, err := tx.Exec(ctx, `UPDATE execution.operations SET cancel_requested=true,state=CASE WHEN state='waiting' THEN 'cancelled' ELSE state END,snapshot=CASE WHEN state='waiting' THEN jsonb_set(snapshot,'{state}','"cancelled"') ELSE snapshot END,acknowledged=acknowledged OR state='waiting',updated_at=clock_timestamp() WHERE environment_id=$1 AND id=$2`, environment, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return execprotocol.ErrDenied
	}
	return nil
}

func (s *Store) ExtendWait(ctx context.Context, environment, id string, wait time.Duration) error {
	result, err := s.exec(ctx, `UPDATE execution.operations SET wait_until=clock_timestamp()+$3*interval '1 millisecond',updated_at=clock_timestamp() WHERE environment_id=$1 AND id=$2 AND state='waiting' AND NOT cancel_requested`, environment, id, wait.Milliseconds())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return execprotocol.ErrConflict
	}
	return nil
}

func (s *Store) Observe(ctx context.Context, environment string, epoch int64, snapshot execprotocol.Snapshot) error {
	if snapshot.Version != execprotocol.Version || snapshot.EnvironmentID != environment || snapshot.NextCursor < int64(len(snapshot.Output)) || snapshot.OutputBytes < snapshot.NextCursor || snapshot.OutputBytes > 8<<20 || len(snapshot.Output) > 256<<10 {
		return execprotocol.ErrInvalid
	}
	fileOperation := snapshot.Kind == "import_file" || snapshot.Kind == "export_file"
	if snapshot.File != nil && (!fileOperation || snapshot.File.Manifest.Validate() != nil || snapshot.File.Cursor < 0 || snapshot.File.Cursor > snapshot.File.Manifest.Size || snapshot.File.Ready && snapshot.File.Cursor != snapshot.File.Manifest.Size) || fileOperation && snapshot.State == execprotocol.Completed && (snapshot.File == nil || !snapshot.File.Ready) {
		return execprotocol.ErrInvalid
	}
	switch snapshot.State {
	case execprotocol.Accepted, execprotocol.Running, execprotocol.Completed, execprotocol.Failed, execprotocol.Cancelled, execprotocol.Unknown:
	default:
		return execprotocol.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, environment, epoch); err != nil {
		return err
	}
	operation, err := scanOperation(tx.QueryRow(ctx, `SELECT `+operationColumns+` FROM execution.operations WHERE environment_id=$1 AND id=$2 FOR UPDATE`, environment, snapshot.ID))
	if err != nil {
		return err
	}
	if snapshot.AgentID != operation.Request.AgentID || snapshot.Kind != operation.Request.Kind {
		return execprotocol.ErrDenied
	}
	start := snapshot.NextCursor - int64(len(snapshot.Output))
	if start > operation.ResultCursor {
		return execprotocol.ErrConflict
	}
	if start < operation.ResultCursor {
		var prior []byte
		overlap := min(operation.ResultCursor-start, int64(len(snapshot.Output)))
		if err := tx.QueryRow(ctx, `SELECT substring(output FROM $3::integer FOR $4::integer) FROM execution.operations WHERE environment_id=$1 AND id=$2`, environment, snapshot.ID, start+1, overlap).Scan(&prior); err != nil {
			return err
		}
		if !bytes.Equal(prior, snapshot.Output[:overlap]) {
			return execprotocol.ErrConflict
		}
		snapshot.Output = snapshot.Output[overlap:]
	}
	if execprotocol.State(operation.State).Terminal() && (snapshot.State != execprotocol.State(operation.State) || snapshot.OutputBytes != operation.Snapshot.OutputBytes) {
		return execprotocol.ErrConflict
	}
	output := snapshot.Output
	snapshot.Output = nil
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE execution.operations SET state=$3,snapshot=$4,output=output||COALESCE($5::bytea,''::bytea),updated_at=clock_timestamp() WHERE environment_id=$1 AND id=$2`, environment, snapshot.ID, snapshot.State, encoded, output); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Settle(ctx context.Context, environment string, epoch int64, id string, state execprotocol.State, message string) error {
	if !state.Terminal() {
		return execprotocol.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, environment, epoch); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE execution.operations SET state=$3,snapshot=jsonb_set(jsonb_set(snapshot,'{state}',to_jsonb($3::text)),'{error}',to_jsonb($4::text)),acknowledged=true,updated_at=clock_timestamp() WHERE environment_id=$1 AND id=$2 AND state IN ('waiting','dispatched','accepted','running')`, environment, id, state, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Acknowledge(ctx context.Context, environment string, epoch int64, id string) error {
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, environment, epoch); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE execution.operations SET acknowledged=true,updated_at=clock_timestamp() WHERE environment_id=$1 AND id=$2 AND state IN ('completed','failed','cancelled','unknown') AND octet_length(output)=(snapshot->>'output_bytes')::bigint`, environment, id)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return execprotocol.ErrConflict
	}
	return tx.Commit(ctx)
}

func (s *Store) Unsettled(ctx context.Context, limit int) ([]execution.Operation, error) {
	if limit < 1 || limit > 1000 {
		return nil, execprotocol.ErrInvalid
	}
	// Rotation by updated_at prevents a permanently offline operation from
	// starving later owners out of the lifecycle reconciliation batch.
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	rows, err := tx.Query(ctx, `UPDATE execution.operations SET updated_at=clock_timestamp() WHERE (environment_id,id) IN (SELECT environment_id,id FROM execution.operations WHERE state IN ('waiting','dispatched','accepted','running') AND NOT cancel_requested ORDER BY updated_at LIMIT $1) RETURNING `+operationColumns, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	operations := []execution.Operation{}
	for rows.Next() {
		operation, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		operations = append(operations, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return operations, tx.Commit(ctx)
}

func (s *Store) ExpireWaiting(ctx context.Context) error {
	_, err := s.exec(ctx, `UPDATE execution.operations SET state='failed',snapshot=jsonb_set(jsonb_set(snapshot,'{state}','"failed"'),'{error}','"environment wait expired before dispatch"'),acknowledged=true,updated_at=clock_timestamp() WHERE state='waiting' AND wait_until<=clock_timestamp()`)
	if err != nil {
		return err
	}
	// Acknowledged output can expire; operation identities and unknown outcomes
	// remain durable so restoring an old caller cannot repeat an external effect.
	_, err = s.exec(ctx, `UPDATE execution.operations SET output='',snapshot=jsonb_set(snapshot,'{output_expired}','true') WHERE acknowledged AND state IN ('completed','failed','cancelled') AND greatest(updated_at,observed_at)<clock_timestamp()-interval '7 days' AND octet_length(output)>0 AND (NOT output_hold OR observed_bytes>=octet_length(output))`)
	if err != nil {
		return err
	}
	_, err = s.exec(ctx, `DELETE FROM execution.pairings WHERE state!='confirmed' AND expires_at<clock_timestamp()-interval '1 day'`)
	return err
}
