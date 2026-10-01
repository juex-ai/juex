package postgres

import (
	"context"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// Operation identity, unknown outcomes and cancellation decisions are not audit
// data and must never be removed by this retention policy.
func (s *Store) PruneAudit(ctx context.Context, days int) error {
	if days < 1 || days > 3650 {
		return execprotocol.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, table := range []string{"audit", "artifact_audit"} {
		if _, err := tx.Exec(ctx, `DELETE FROM execution.`+table+` WHERE id IN (SELECT id FROM execution.`+table+` WHERE created_at<clock_timestamp()-make_interval(days=>$1) ORDER BY created_at,id LIMIT 10000)`, days); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
