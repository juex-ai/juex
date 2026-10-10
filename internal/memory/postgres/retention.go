package postgres

import (
	"context"
	"encoding/json"
	"github.com/juex-ai/juex/internal/foundation/application"
	"time"
)

func (s *Store) ExpiredReviewEvidence(ctx context.Context, before time.Time, limit int) ([]application.Scope, error) {
	if limit < 1 || limit > 100 {
		return nil, application.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT ON(f.id) r.value->'scope' FROM memory.fleets f CROSS JOIN LATERAL jsonb_each(f.state->'reviews') r
 WHERE NOT r.value ? 'imported' AND COALESCE((r.value->>'worker_finished')::boolean,false) AND (r.value->'receipt'->>'updated_at')::timestamptz<$1 AND COALESCE(r.value->'proposal'->>'text','')<>'' ORDER BY f.id LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []application.Scope
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var scope application.Scope
		if err := json.Unmarshal(data, &scope); err != nil {
			return nil, err
		}
		values = append(values, scope)
	}
	return values, rows.Err()
}
