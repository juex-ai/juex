package postgres

import (
	"context"
	"encoding/json"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/memory"
)

func (s *Store) PendingReviews(ctx context.Context, limit int) ([]memory.Review, error) {
	if limit < 1 || limit > 100 {
		return nil, application.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT review.value FROM memory.fleets f CROSS JOIN LATERAL jsonb_each(f.state->'reviews') review
 WHERE COALESCE((review.value->>'worker_finished')::boolean,false)=false
 ORDER BY COALESCE(review.value->>'attempted_at',''),f.updated_at,review.key LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []memory.Review
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var value memory.Review
		if err := json.Unmarshal(data, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}
