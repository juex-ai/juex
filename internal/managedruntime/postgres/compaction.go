package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/hookpolicy"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func readCompaction(ctx context.Context, tx pgx.Tx, turn string) (*managedruntime.CompactionJob, error) {
	var job managedruntime.CompactionJob
	err := tx.QueryRow(ctx, `SELECT id,turn_id,source_generation,source_sequence,attempts,reason,focus FROM runtime.compactions WHERE turn_id=$1 AND state='pending'`, turn).Scan(&job.ID, &job.TurnID, &job.SourceGeneration, &job.SourceSequence, &job.Attempts, &job.Reason, &job.Focus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &job, err
}

func contextSequence(ctx context.Context, tx pgx.Tx, thread string) (int64, error) {
	var sequence int64
	err := tx.QueryRow(ctx, `SELECT COALESCE(max(sequence),0) FROM runtime.events WHERE thread_id=$1 AND kind='message.appended'`, thread).Scan(&sequence)
	return sequence, err
}

func (s *Store) PrepareCompaction(ctx context.Context, lease managedruntime.Lease, work managedruntime.Work, reason, focus string) (*managedruntime.CompactionJob, error) {
	if reason != "automatic" && reason != "manual" || len(focus) > 4096 {
		return nil, managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, lease); err != nil {
		return nil, err
	}
	thread, err := readThread(ctx, tx, lease.AgentID, work.ThreadID)
	if err != nil {
		return nil, err
	}
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT generation FROM runtime.turns WHERE id=$1 AND thread_id=$2 AND activation_epoch=$3 AND state='running'`, work.TurnID, thread.ID, lease.Epoch).Scan(&generation); err != nil {
		return nil, classify(err)
	}
	var started bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.attempts WHERE turn_id=$1 AND state='started')`, work.TurnID).Scan(&started); err != nil {
		return nil, err
	}
	if started {
		return nil, managedruntime.ErrConflict
	}
	sequence, err := contextSequence(ctx, tx, thread.ID)
	if err != nil {
		return nil, err
	}
	if generation != work.Generation || thread.Generation != generation || sequence != work.ContextSequence {
		return nil, managedruntime.ErrConflict
	}
	job, err := readCompaction(ctx, tx, work.TurnID)
	if err != nil {
		return nil, err
	}
	if job != nil {
		return job, tx.Commit(ctx)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.compactions(turn_id,thread_id,source_generation,source_sequence,reason,focus) VALUES($1,$2,$3,$4,$5,$6)`, work.TurnID, thread.ID, generation, sequence, reason, focus); err != nil {
		return nil, classify(err)
	}
	job, err = readCompaction(ctx, tx, work.TurnID)
	if err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, tx, thread.ID, "context.compacting", map[string]any{"job_id": job.ID, "turn_id": work.TurnID, "reason": reason}); err != nil {
		return nil, err
	}
	return job, tx.Commit(ctx)
}

func admitCompactionAttempt(ctx context.Context, tx pgx.Tx, turn string, request managedruntime.ModelRequest) error {
	job, err := readCompaction(ctx, tx, turn)
	if err != nil {
		return err
	}
	if request.Purpose != "compaction" {
		if request.Compaction != nil || job != nil {
			return managedruntime.ErrConflict
		}
		return nil
	}
	draft := request.Compaction
	if job == nil || draft == nil || job.ID != draft.JobID || job.SourceGeneration != request.Generation || job.SourceGeneration != draft.SourceGeneration || job.SourceSequence != draft.SourceSequence {
		return managedruntime.ErrConflict
	}
	if job.Attempts >= 6 {
		return managedruntime.ErrCompactionFailed
	}
	_, err = tx.Exec(ctx, `UPDATE runtime.compactions SET attempts=attempts+1 WHERE id=$1`, job.ID)
	return err
}

func finishCompaction(ctx context.Context, tx pgx.Tx, turn, thread, input string, request managedruntime.ModelRequest, response llm.Response, failure string) error {
	draft := request.Compaction
	job, err := readCompaction(ctx, tx, turn)
	if err != nil {
		return err
	}
	if job == nil || draft == nil || job.ID != draft.JobID {
		return managedruntime.ErrConflict
	}
	if failure == "cancelled" {
		if err := cancelCompaction(ctx, tx, turn); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.inputs SET state='cancelled' WHERE id=$1`, input); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET state='cancelled',completed_at=clock_timestamp() WHERE id=$1`, turn); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state=CASE WHEN EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=$1 AND state='queued') THEN 'queued' ELSE 'idle' END WHERE id=$1`, thread); err != nil {
			return err
		}
		return appendEvent(ctx, tx, thread, "turn.cancelled", map[string]string{"turn_id": turn, "input_id": input, "error": "cancelled"})
	}
	if failure != "" {
		if _, err := tx.Exec(ctx, `UPDATE runtime.compactions SET state='failed',completed_at=clock_timestamp() WHERE id=$1`, job.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.inputs SET state='held' WHERE id=$1`, input); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET state='failed',completed_at=clock_timestamp() WHERE id=$1`, turn); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state=CASE WHEN EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=$1 AND state='queued') THEN 'queued' ELSE 'failed' END WHERE id=$1`, thread); err != nil {
			return err
		}
		return appendEvent(ctx, tx, thread, "input.held", map[string]string{"input_id": input, "reason": "compaction_failed"})
	}
	sequence, err := contextSequence(ctx, tx, thread)
	if err != nil {
		return err
	}
	if sequence != job.SourceSequence {
		return managedruntime.ErrConflict
	}
	var generation int64
	err = tx.QueryRow(ctx, `UPDATE runtime.threads SET generation=generation+1,state='queued' WHERE id=$1 AND generation=$2 RETURNING generation`, thread, job.SourceGeneration).Scan(&generation)
	if err != nil {
		return classify(err)
	}
	result, err := tx.Exec(ctx, `UPDATE runtime.turns SET generation=$2 WHERE id=$1 AND state='running' AND generation=$3`, turn, generation, job.SourceGeneration)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrConflict
	}
	summary := llm.TextMessage(llm.RoleUser, managedruntime.CompactionText(response))
	summary.ID = response.Message.ID
	summary.Kind = llm.MessageKindCompact
	summary.Model = response.Message.Model
	summary.Compaction = &llm.CompactionMetadata{Auto: job.Reason == "automatic", Reason: job.Reason, TokensBefore: draft.BeforeTokens, SummaryModel: summary.Model, SummaryChars: len(summary.FirstText()), RetainedMessageIDs: draft.RetainedIDs}
	summary.Compaction.TokensAfter = llm.EstimateContextTokens(draft.ConversationSystem, draft.ConversationTools, append([]llm.Message{summary}, draft.Retained...))
	if err := appendEvent(ctx, tx, thread, "message.appended", summary); err != nil {
		return err
	}
	ids := append([]string{summary.ID}, draft.RetainedIDs...)
	if _, err := tx.Exec(ctx, `INSERT INTO runtime.context_checkpoints(thread_id,generation,compaction_id,message_ids,through_sequence) SELECT $1,$2,$3,$4,sequence FROM runtime.threads WHERE id=$1`, thread, generation, job.ID, ids); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.compactions SET state='completed',completed_at=clock_timestamp() WHERE id=$1`, job.ID); err != nil {
		return err
	}
	var source []byte
	if err := tx.QueryRow(ctx, `SELECT source FROM runtime.inputs WHERE id=$1`, input).Scan(&source); err != nil {
		return err
	}
	var origin managedruntime.InputSource
	if err := json.Unmarshal(source, &origin); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, thread, "context.compacted", map[string]any{"job_id": job.ID, "turn_id": turn, "generation": generation, "tokens_before": draft.BeforeTokens, "tokens_after": summary.Compaction.TokensAfter}); err != nil {
		return err
	}
	if err := enqueueHooks(ctx, tx, turn, hookpolicy.PostCompact, job.ID, managedruntime.HookInput{CompactReason: job.Reason, CompactAuto: job.Reason == "automatic"}); err != nil {
		return err
	}
	decision, err := hookDecision(ctx, tx, turn, hookpolicy.PostCompact, job.ID)
	if err != nil {
		return err
	}
	if !decision.Ready {
		return deferHookFinish(ctx, tx, turn, thread, hookFinish{Event: hookpolicy.PostCompact, Anchor: job.ID, Resume: origin.Kind != "compaction"})
	}
	if origin.Kind == "compaction" {
		if _, err := tx.Exec(ctx, `UPDATE runtime.turns SET state='completed',completed_at=clock_timestamp() WHERE id=$1`, turn); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.inputs SET state='completed' WHERE id=$1`, input); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state=CASE WHEN EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=$1 AND state='queued') THEN 'queued' ELSE 'idle' END WHERE id=$1`, thread); err != nil {
			return err
		}
	}
	return nil
}

func cancelCompaction(ctx context.Context, tx pgx.Tx, turn string) error {
	_, err := tx.Exec(ctx, `UPDATE runtime.compactions SET state='cancelled',completed_at=clock_timestamp() WHERE turn_id=$1 AND state='pending'`, turn)
	return err
}

// A manual request on a short context completes without consuming model usage.
func (s *Store) SkipCompaction(ctx context.Context, lease managedruntime.Lease, work managedruntime.Work) error {
	if work.Source.Kind != "compaction" {
		return managedruntime.ErrInvalid
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err := fence(ctx, tx, lease); err != nil {
		return err
	}
	thread, err := readThread(ctx, tx, lease.AgentID, work.ThreadID)
	if err != nil {
		return err
	}
	job, err := readCompaction(ctx, tx, work.TurnID)
	if err != nil {
		return err
	}
	if job == nil || job.Reason != "manual" || thread.Generation != job.SourceGeneration {
		return managedruntime.ErrConflict
	}
	var started bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.attempts WHERE turn_id=$1 AND state='started')`, work.TurnID).Scan(&started); err != nil {
		return err
	}
	if started {
		return managedruntime.ErrConflict
	}
	sequence, err := contextSequence(ctx, tx, thread.ID)
	if err != nil {
		return err
	}
	if sequence != job.SourceSequence {
		return managedruntime.ErrConflict
	}
	result, err := tx.Exec(ctx, `UPDATE runtime.turns SET state='completed',completed_at=clock_timestamp() WHERE id=$1 AND input_id=$2 AND activation_epoch=$3 AND state='running'`, work.TurnID, work.InputID, lease.Epoch)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrConflict
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.inputs SET state='completed' WHERE id=$1`, work.InputID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.compactions SET state='completed',completed_at=clock_timestamp() WHERE id=$1`, job.ID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime.threads SET state=CASE WHEN EXISTS(SELECT 1 FROM runtime.inputs WHERE thread_id=$1 AND state='queued') THEN 'queued' ELSE 'idle' END WHERE id=$1`, thread.ID); err != nil {
		return err
	}
	if err := appendEvent(ctx, tx, thread.ID, "context.unchanged", map[string]string{"job_id": job.ID, "turn_id": work.TurnID}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
