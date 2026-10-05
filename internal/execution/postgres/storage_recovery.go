package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/execution"
)

// RecoveryAllocations is an offline operator inventory, not a cross-service API.
func (s *Store) RecoveryAllocations(ctx context.Context) ([]execution.ManagedResource, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+managedColumns+` FROM execution.managed_environments h JOIN execution.environments e ON e.id=h.environment_id WHERE h.backend='gvisor' ORDER BY h.environment_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []execution.ManagedResource
	for rows.Next() {
		v, err := scanManaged(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, v)
	}
	return result, rows.Err()
}

// RebindRecoveredStorage runs only after offline filesystem validation. Retrying
// after interruption accepts the same pair of identities, never a third pool.
func (s *Store) RebindRecoveredStorage(ctx context.Context, previous, current string) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var mismatch bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution.managed_environments WHERE backend='gvisor' AND storage_identity NOT IN ($1::uuid,$2::uuid))`, previous, current).Scan(&mismatch); err != nil {
		return err
	}
	if mismatch {
		return errors.New("recovery contains a different storage pool")
	}
	if _, err = tx.Exec(ctx, `UPDATE execution.managed_environments SET storage_identity=$1,running=false WHERE backend='gvisor'`, current); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE execution.environments SET online_until=NULL,connection_epoch=connection_epoch+1`); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
