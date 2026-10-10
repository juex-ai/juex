package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/agentpolicy"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) Inspection(ctx context.Context, scope managedruntime.Scope, threadID string) (managedruntime.ThreadInspection, error) {
	result := managedruntime.ThreadInspection{Capabilities: scope.Capabilities.Normalized()}
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
	result.InputChecklist, err = readInputChecklist(ctx, tx, threadID, scope.Capabilities.Allows(agentpolicy.InputTracking))
	if err != nil {
		return result, err
	}
	result.State, err = readThreadState(ctx, tx, threadID)
	if err != nil {
		return result, err
	}
	result.WorkingFiles, err = readWorkingFiles(ctx, tx, threadID)
	if err != nil {
		return result, err
	}
	var encoded []byte
	var record managedruntime.RequestInspection
	err = tx.QueryRow(ctx, `SELECT a.id,a.turn_id,a.started_at,a.request FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=$1 AND (a.request->>'generation')::bigint=$2 ORDER BY a.started_at DESC,a.ordinal DESC LIMIT 1`, threadID, result.Thread.Generation).Scan(&record.AttemptID, &record.TurnID, &record.RecordedAt, &encoded)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	}
	if err == nil {
		var request managedruntime.ModelRequest
		if err := json.Unmarshal(encoded, &request); err != nil {
			return result, err
		}
		value := request.Inspection()
		value.AttemptID, value.TurnID, value.RecordedAt = record.AttemptID, record.TurnID, record.RecordedAt
		result.LatestRequest = &value
	}
	// Attempts are retained with the Thread, independently of raw usage retention.
	// Imported messages have no attempts; expose that gap rather than zero usage.
	err = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE a.usage_status='complete'),count(*) FILTER(WHERE a.usage_status='partial'),count(*) FILTER(WHERE a.usage_status='unknown'),COALESCE(sum((a.usage->>'input_tokens')::bigint) FILTER(WHERE a.usage_status<>'unknown'),0)::bigint,COALESCE(sum((a.usage->>'output_tokens')::bigint) FILTER(WHERE a.usage_status<>'unknown'),0)::bigint,COALESCE(sum((a.usage->>'cached_input_tokens')::bigint) FILTER(WHERE a.usage_status<>'unknown'),0)::bigint FROM runtime.attempts a JOIN runtime.turns t ON t.id=a.turn_id WHERE t.thread_id=$1`, threadID).Scan(usageTargets(&result.Usage)...)
	if err != nil {
		return result, err
	}
	result.Usage.TotalTokens = result.Usage.InputTokens + result.Usage.OutputTokens
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.context_checkpoints WHERE thread_id=$1 AND import_agent_id IS NOT NULL)`, threadID).Scan(&result.ImportedHistory); err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
