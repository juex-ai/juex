package postgres

import (
	"context"
	"slices"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) History(ctx context.Context, scope managedruntime.Scope, threadID string, before int64, limit int) (managedruntime.Timeline, error) {
	result := managedruntime.Timeline{Events: []managedruntime.Event{}}
	if before < 0 || limit < 1 || limit > 500 {
		return result, managedruntime.ErrInvalid
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, scope); err != nil {
		return result, err
	}
	result.Thread, err = scanThread(tx.QueryRow(ctx, `SELECT `+threadColumns+` FROM runtime.threads t WHERE t.agent_id=$1 AND t.id=$2`, scope.AgentID, threadID))
	if err != nil {
		return result, err
	}
	// A page can start inside a Turn. Carry its identity without fetching the
	// entire Turn, which can contain an unbounded number of tool events.
	rows, err := tx.Query(ctx, `SELECT e.id,e.thread_id,e.sequence,e.generation,e.kind,e.data,e.created_at,
COALESCE((SELECT data->>'turn_id' FROM runtime.events start WHERE start.thread_id=e.thread_id AND start.generation=e.generation AND start.kind='turn.started' AND start.sequence<=e.sequence ORDER BY start.sequence DESC LIMIT 1),'')
FROM runtime.events e WHERE e.thread_id=$1 AND ($2::bigint=0 OR e.sequence<$2) ORDER BY e.sequence DESC LIMIT $3`, threadID, before, limit+1)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var event managedruntime.Event
		if err := rows.Scan(&event.ID, &event.ThreadID, &event.Sequence, &event.Generation, &event.Kind, &event.Data, &event.CreatedAt, &event.TurnID); err != nil {
			rows.Close()
			return result, err
		}
		result.Events = append(result.Events, event)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return result, err
	}
	result.HasPrevious = len(result.Events) > limit
	if result.HasPrevious {
		result.Events = result.Events[:limit]
	}
	slices.Reverse(result.Events)
	if len(result.Events) > 0 {
		result.PreviousSequence = result.Events[0].Sequence
	}
	result.NextSequence = result.Thread.Sequence
	if err := enrichToolAttempts(ctx, tx, threadID, result.Events); err != nil {
		return result, err
	}
	if before == 0 {
		result.Progress, err = readProgress(ctx, tx, result.Thread)
		if err != nil {
			return result, err
		}
	}
	return result, tx.Commit(ctx)
}
