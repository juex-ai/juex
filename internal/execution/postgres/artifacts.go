package postgres

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

const artifactColumns = `id,scope,request,state,created_at`

func scanArtifact(row pgx.Row) (execution.Artifact, error) {
	var artifact execution.Artifact
	err := row.Scan(&artifact.ID, &artifact.Scope, &artifact.Request, &artifact.State, &artifact.CreatedAt)
	return artifact, classify(err)
}

func artifactAudit(ctx context.Context, tx pgx.Tx, scope execution.Scope, id, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO execution.artifact_audit(tenant_id,owner_id,actor_id,agent_id,artifact_id,action) VALUES($1,$2,$3,$4,$5,$6)`, scope.TenantID, scope.UserID, scope.ActorID, scope.AgentID, id, action)
	return err
}

func (s *Store) ReserveArtifact(ctx context.Context, scope execution.Scope, request execution.ArtifactRequest, limit int64) (execution.Artifact, error) {
	if err := request.Validate(); err != nil {
		return execution.Artifact{}, err
	}
	if !scope.CanExecute {
		return execution.Artifact{}, execprotocol.ErrDenied
	}
	if limit < 1 {
		return execution.Artifact{}, execprotocol.ErrInvalid
	}
	hash, err := execution.CanonicalHash(request)
	if err != nil {
		return execution.Artifact{}, err
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Artifact{}, err
	}
	defer rollback(tx)
	// Every object, including unfinished uploads and purges, reserves bytes in
	// the same local volume. Serializing reservations prevents cross-user races.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('juex.execution.blob.capacity'))`); err != nil {
		return execution.Artifact{}, err
	}
	var storedHash string
	err = tx.QueryRow(ctx, `SELECT request_hash FROM execution.artifacts WHERE agent_id=$1 AND request_id=$2`, scope.AgentID, request.RequestID).Scan(&storedHash)
	if err == nil {
		artifact, err := scanArtifact(tx.QueryRow(ctx, `SELECT `+artifactColumns+` FROM execution.artifacts WHERE agent_id=$1 AND request_id=$2`, scope.AgentID, request.RequestID))
		if err != nil {
			return artifact, err
		}
		if !artifact.Scope.SameAuthority(scope) {
			return artifact, execprotocol.ErrDenied
		}
		if storedHash != hash || artifact.State == "purging" || artifact.State == "deleted" {
			return artifact, execprotocol.ErrConflict
		}
		return artifact, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return execution.Artifact{}, err
	}
	var reserved, objects int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sum(size),0)::bigint,count(*) FROM execution.artifacts WHERE state!='deleted'`).Scan(&reserved, &objects); err != nil {
		return execution.Artifact{}, err
	}
	if request.Manifest.Size > limit-reserved || objects >= 65536 {
		return execution.Artifact{}, execprotocol.ErrQuota
	}
	artifact, err := scanArtifact(tx.QueryRow(ctx, `INSERT INTO execution.artifacts(id,tenant_id,user_id,fleet_id,agent_id,request_id,scope,request,request_hash,visibility,size) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+artifactColumns, uuid.NewString(), scope.TenantID, scope.UserID, scope.FleetID, scope.AgentID, request.RequestID, scope, request, hash, request.Visibility, request.Manifest.Size))
	if err != nil {
		return artifact, err
	}
	if err := artifactAudit(ctx, tx, scope, artifact.ID, "artifact.upload.accepted"); err != nil {
		return artifact, err
	}
	return artifact, tx.Commit(ctx)
}

func (s *Store) Artifact(ctx context.Context, scope execution.Scope, id string) (execution.Artifact, error) {
	artifact, err := scanArtifact(s.pool.QueryRow(ctx, `SELECT `+artifactColumns+` FROM execution.artifacts WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND fleet_id=$4`, id, scope.TenantID, scope.UserID, scope.FleetID))
	if err != nil {
		return execution.Artifact{}, err
	}
	if !artifact.ReadableBy(scope) {
		return execution.Artifact{}, execprotocol.ErrDenied
	}
	return artifact, nil
}

func (s *Store) Artifacts(ctx context.Context, scope execution.Scope, after string, limit int) ([]execution.Artifact, error) {
	if limit < 1 || limit > 100 {
		return nil, execprotocol.ErrInvalid
	}
	if after == "" {
		after = uuid.Nil.String()
	}
	rows, err := s.pool.Query(ctx, `SELECT `+artifactColumns+` FROM execution.artifacts WHERE tenant_id=$1 AND user_id=$2 AND fleet_id=$3 AND state IN ('uploading','ready') AND (agent_id=$4 OR state='ready' AND visibility='fleet') AND id>$5 ORDER BY id LIMIT $6`, scope.TenantID, scope.UserID, scope.FleetID, scope.AgentID, after, limit)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()
	result := []execution.Artifact{}
	for rows.Next() {
		artifact, err := scanArtifact(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, artifact)
	}
	return result, rows.Err()
}

func (s *Store) PublishArtifact(ctx context.Context, scope execution.Scope, id string) (execution.Artifact, error) {
	if !scope.CanExecute {
		return execution.Artifact{}, execprotocol.ErrDenied
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Artifact{}, err
	}
	defer rollback(tx)
	artifact, err := scanArtifact(tx.QueryRow(ctx, `SELECT `+artifactColumns+` FROM execution.artifacts WHERE id=$1 FOR UPDATE`, id))
	if err != nil {
		return execution.Artifact{}, err
	}
	if !artifact.Scope.SameAuthority(scope) {
		return execution.Artifact{}, execprotocol.ErrDenied
	}
	if artifact.State != "uploading" && artifact.State != "ready" {
		return execution.Artifact{}, execprotocol.ErrConflict
	}
	if artifact.State == "uploading" {
		if err := artifactAudit(ctx, tx, scope, id, "artifact.published"); err != nil {
			return execution.Artifact{}, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE execution.artifacts SET state='ready' WHERE id=$1`, id); err != nil {
		return execution.Artifact{}, err
	}
	artifact.State = "ready"
	return artifact, tx.Commit(ctx)
}

func (s *Store) BeginArtifactPurge(ctx context.Context, scope execution.Scope, id string) (execution.Artifact, error) {
	if !scope.CanExecute {
		return execution.Artifact{}, execprotocol.ErrDenied
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return execution.Artifact{}, err
	}
	defer rollback(tx)
	artifact, err := scanArtifact(tx.QueryRow(ctx, `SELECT `+artifactColumns+` FROM execution.artifacts WHERE id=$1 AND tenant_id=$2 AND user_id=$3 AND fleet_id=$4 AND agent_id=$5 FOR UPDATE`, id, scope.TenantID, scope.UserID, scope.FleetID, scope.AgentID))
	if err != nil {
		return execution.Artifact{}, err
	}
	if artifact.State != "purging" && artifact.State != "deleted" {
		if _, err := tx.Exec(ctx, `UPDATE execution.artifacts SET state='purging' WHERE id=$1`, id); err != nil {
			return execution.Artifact{}, err
		}
		if err := artifactAudit(ctx, tx, scope, id, "artifact.purge.requested"); err != nil {
			return execution.Artifact{}, err
		}
		artifact.State = "purging"
	}
	return artifact, tx.Commit(ctx)
}

func (s *Store) FinishArtifactPurge(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE execution.artifacts SET state='deleted',request='{}',request_hash='',scope=scope-'owner_email'-'tenant_name' WHERE id=$1 AND state IN ('purging','deleted')`, id)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return execprotocol.ErrConflict
	}
	return nil
}

func (s *Store) ArtifactPurges(ctx context.Context, limit int) ([]string, error) {
	if limit < 1 || limit > 100 {
		return nil, execprotocol.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT id FROM execution.artifacts WHERE state='purging' ORDER BY created_at,id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
