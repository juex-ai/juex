package postgres

import (
	"context"

	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) ToolEvidence(ctx context.Context, work managedruntime.ToolWork) (managedruntime.OriginalEvidence, error) {
	var value managedruntime.OriginalEvidence
	tx, err := s.begin(ctx)
	if err != nil {
		return value, err
	}
	defer rollback(tx)
	if err := checkScope(ctx, tx, work.Scope); err != nil {
		return value, err
	}
	if err := subscriptionAction(ctx, tx, work); err != nil {
		return value, err
	}
	err = tx.QueryRow(ctx, `SELECT i.text,e.sequence,e.generation,i.accepted_at
 FROM runtime.inputs i JOIN runtime.turns t ON t.input_id=i.id
 JOIN runtime.events e ON e.thread_id=i.thread_id AND e.kind='input.accepted' AND e.data->'receipt'->>'id'=i.id::text
 WHERE t.id=$1 AND t.thread_id=$2 AND COALESCE(i.source->>'kind','')='' AND octet_length(i.text)<=32768`, work.TurnID, work.ThreadID).Scan(&value.Text, &value.Sequence, &value.Generation, &value.RecordedAt)
	if err != nil {
		return value, classify(err)
	}
	return value, tx.Commit(ctx)
}
