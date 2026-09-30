package postgres

import (
	"context"
	"encoding/json"

	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/memory"
)

func (s *Store) PendingParticipation(ctx context.Context, limit int) ([]memory.Participation, error) {
	if limit < 1 || limit > 100 {
		return nil, application.ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT p.value FROM memory.fleets f CROSS JOIN LATERAL jsonb_each(f.state->'participation') p
 WHERE (f.state->'control'->>'enabled')::boolean AND f.state->>'strategy'='advanced' AND jsonb_array_length(COALESCE(NULLIF(p.value->'evidence','null'::jsonb),'[]'::jsonb))>0
 ORDER BY p.value->>'attempted_at',f.id,p.key LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []memory.Participation
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var p memory.Participation
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		values = append(values, p)
	}
	return values, rows.Err()
}
