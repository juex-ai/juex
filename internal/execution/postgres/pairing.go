package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

const pairColumns = `id,environment_id,name,os,working_directory,capabilities,state,expires_at,owner_scope,grants,agent_epochs,approval_nonce`

func scanPair(row pgx.Row) (execution.Pairing, error) {
	var pair execution.Pairing
	err := row.Scan(&pair.ID, &pair.EnvironmentID, &pair.Name, &pair.OS, &pair.WorkingDirectory, &pair.Capabilities, &pair.State, &pair.ExpiresAt, &pair.Owner, &pair.Grants, &pair.AgentEpochs, &pair.ApprovalNonce)
	return pair, classify(err)
}

func (s *Store) BeginPair(ctx context.Context, request execution.PairRequest) (execution.Pairing, error) {
	hash, err := execution.CanonicalHash(request)
	if err != nil {
		return execution.Pairing{}, err
	}
	capabilities, err := json.Marshal(request.Capabilities)
	if err != nil {
		return execution.Pairing{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Pairing{}, err
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, `INSERT INTO execution.pairings(id,request_hash,pair_secret_hash,credential_hash,name,os,working_directory,capabilities) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO NOTHING`, request.ID, hash, request.PairSecretHash, request.CredentialHash, request.Name, request.OS, request.WorkingDirectory, capabilities); err != nil {
		return execution.Pairing{}, classify(err)
	}
	pair, err := scanPair(tx.QueryRow(ctx, `SELECT `+pairColumns+` FROM execution.pairings WHERE id=$1 AND request_hash=$2`, request.ID, hash))
	if err != nil {
		if errors.Is(err, execprotocol.ErrDenied) {
			err = execprotocol.ErrConflict
		}
		return pair, err
	}
	return pair, tx.Commit(ctx)
}

func (s *Store) Pair(ctx context.Context, id, secretHash string) (execution.Pairing, error) {
	return scanPair(s.pool.QueryRow(ctx, `SELECT `+pairColumns+` FROM execution.pairings WHERE id=$1 AND ($2='' OR pair_secret_hash=$2) AND (expires_at>clock_timestamp() OR state='confirmed')`, id, secretHash))
}

func (s *Store) ApprovePair(ctx context.Context, pair execution.Pairing) (execution.Pairing, error) {
	owner, err := json.Marshal(pair.Owner)
	if err != nil {
		return execution.Pairing{}, err
	}
	grants, err := json.Marshal(pair.Grants)
	if err != nil {
		return execution.Pairing{}, err
	}
	epochs, err := json.Marshal(pair.AgentEpochs)
	if err != nil {
		return execution.Pairing{}, err
	}
	return scanPair(s.pool.QueryRow(ctx, `UPDATE execution.pairings SET state='approved',owner_scope=$2,grants=$3,agent_epochs=$4,approval_nonce=$5 WHERE id=$1 AND state='pending' AND expires_at>clock_timestamp() RETURNING `+pairColumns, pair.ID, owner, grants, epochs, pair.ApprovalNonce))
}

func (s *Store) ConfirmPair(ctx context.Context, confirmation execution.PairConfirmation) (execution.Device, error) {
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Device{}, err
	}
	defer rollback(tx)
	pair, err := scanPair(tx.QueryRow(ctx, `SELECT `+pairColumns+` FROM execution.pairings WHERE id=$1 AND pair_secret_hash=$2 AND credential_hash=$3 AND approval_nonce=$4 AND state IN ('approved','confirmed') AND (expires_at>clock_timestamp() OR state='confirmed') FOR UPDATE`, confirmation.ID, execution.Digest(confirmation.Secret), execution.Digest(confirmation.Credential), confirmation.ApprovalNonce))
	if err != nil {
		return execution.Device{}, err
	}
	if pair.State != "confirmed" {
		grants, err := json.Marshal(pair.Grants)
		if err != nil {
			return execution.Device{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO execution.environments(id,tenant_id,user_id,fleet_id,kind,name,os,working_directory,credential_hash,removal_epoch,grants,ceiling) VALUES($1,$2,$3,$4,'native',$5,$6,$7,$8,$9,$10,$10)`, pair.EnvironmentID, pair.Owner.TenantID, pair.Owner.UserID, pair.Owner.FleetID, pair.Name, pair.OS, pair.WorkingDirectory, execution.Digest(confirmation.Credential), pair.Owner.RemovalEpoch, grants); err != nil {
			return execution.Device{}, classify(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE execution.pairings SET state='confirmed' WHERE id=$1`, pair.ID); err != nil {
			return execution.Device{}, err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO execution.audit(tenant_id,owner_id,actor_id,environment_id,action,version) VALUES($1,$2,$2,$3,'device.paired',1)`, pair.Owner.TenantID, pair.Owner.UserID, pair.EnvironmentID); err != nil {
			return execution.Device{}, err
		}
	}
	device, err := scanDevice(tx.QueryRow(ctx, `SELECT `+deviceColumns+` FROM execution.environments WHERE id=$1`, pair.EnvironmentID))
	if err != nil {
		return device, err
	}
	return device, tx.Commit(ctx)
}
