package postgres

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func recallInput(ctx context.Context, tx pgx.Tx, lease managedruntime.Lease, work managedruntime.Work) (string, error) {
	if err := fence(ctx, tx, lease); err != nil {
		return "", err
	}
	if lease.AgentID != work.Scope.AgentID {
		return "", managedruntime.ErrDenied
	}
	var text string
	err := tx.QueryRow(ctx, `SELECT i.text FROM runtime.inputs i JOIN runtime.turns t ON t.input_id=i.id JOIN runtime.threads th ON th.id=t.thread_id WHERE t.id=$1 AND i.id=$2 AND th.id=$3 AND th.agent_id=$4 AND t.activation_epoch=$5 AND t.state='running' AND COALESCE(i.source->>'kind','')=''`, work.TurnID, work.InputID, work.ThreadID, lease.AgentID, lease.Epoch).Scan(&text)
	return text, classify(err)
}

func (s *Store) BeginRecall(ctx context.Context, lease managedruntime.Lease, work managedruntime.Work) (managedruntime.RecallSnapshot, string, bool, error) {
	var value managedruntime.RecallSnapshot
	tx, err := s.begin(ctx)
	if err != nil {
		return value, "", false, err
	}
	defer rollback(tx)
	text, err := recallInput(ctx, tx, lease, work)
	if err != nil {
		return value, "", false, err
	}
	tag, err := tx.Exec(ctx, `INSERT INTO runtime.memory_recall(input_id,activation_epoch) VALUES($1,$2) ON CONFLICT DO NOTHING`, work.InputID, lease.Epoch)
	if err != nil {
		return value, "", false, err
	}
	var data []byte
	if err := tx.QueryRow(ctx, `SELECT snapshot FROM runtime.memory_recall WHERE input_id=$1 FOR UPDATE`, work.InputID).Scan(&data); err != nil {
		return value, "", false, err
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return value, "", false, err
	}
	prepare := tag.RowsAffected() == 1
	if !prepare && value.State == "preparing" {
		value = managedruntime.RecallSnapshot{State: "unavailable"}
		if _, err := tx.Exec(ctx, `UPDATE runtime.memory_recall SET snapshot='{"state":"unavailable"}' WHERE input_id=$1`, work.InputID); err != nil {
			return value, "", false, err
		}
		if err := appendEvent(ctx, tx, work.ThreadID, "memory.recall_unavailable", map[string]string{"input_id": work.InputID}); err != nil {
			return value, "", false, err
		}
	}
	return value, text, prepare, tx.Commit(ctx)
}

func (s *Store) FinishRecall(ctx context.Context, lease managedruntime.Lease, work managedruntime.Work, value managedruntime.RecallSnapshot) error {
	if len(value.Text) > 4096 || value.State != "ready" && value.State != "unavailable" {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err := recallInput(ctx, tx, lease, work); err != nil {
		return err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE runtime.memory_recall SET snapshot=$3 WHERE input_id=$1 AND activation_epoch=$2 AND snapshot->>'state'='preparing'`, work.InputID, lease.Epoch, data)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return managedruntime.ErrConflict
	}
	if value.State == "unavailable" {
		if err := appendEvent(ctx, tx, work.ThreadID, "memory.recall_unavailable", map[string]string{"input_id": work.InputID}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
