package postgres

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func (s *Store) WriteOrigin(ctx context.Context, work managedruntime.ToolWork, id string) (managedruntime.WriteOrigin, error) {
	var origin managedruntime.WriteOrigin
	tx, err := s.begin(ctx)
	if err != nil {
		return origin, err
	}
	defer rollback(tx)
	err = tx.QueryRow(ctx, `SELECT j.environment_id,j.request FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id JOIN runtime.threads th ON th.id=t.thread_id WHERE j.id::text=$1 AND t.thread_id=$2 AND th.agent_id=$3 AND j.request->>'kind'='write_begin' AND j.request->'write_context'->>'thread_id'=th.id::text AND j.request->'write_context'->>'reset_id'=th.input_scope::text`, id, work.ThreadID, work.Scope.AgentID).Scan(&origin.EnvironmentID, &origin.Request)
	if err != nil {
		return origin, classify(err)
	}
	return origin, tx.Commit(ctx)
}

func readActiveWrites(ctx context.Context, tx pgx.Tx, work managedruntime.Work) ([]managedruntime.ActiveWrite, error) {
	rows, err := tx.Query(ctx, `SELECT j.scope,j.environment_id,j.request,COALESCE(j.result->'result_fact',j.deferred_result->'ResultFact') FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE t.thread_id=$1 AND j.request->'write_context'->>'reset_id'=$2 ORDER BY j.created_at,j.id`, work.ThreadID, work.InputScopeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type session struct {
		view     managedruntime.ActiveWrite
		chunks   map[int]int64
		terminal bool
	}
	sessions := map[string]*session{}
	for rows.Next() {
		var scope managedruntime.Scope
		var env string
		var request execprotocol.Request
		var fact *llm.ResultFact
		if err := rows.Scan(&scope, &env, &request, &fact); err != nil {
			return nil, err
		}
		if !scope.SameAuthority(work.Scope) || fact == nil || fact.Owner != "chunked-write" {
			continue
		}
		var value managedruntime.WriteFact
		if json.Unmarshal(fact.Data, &value) != nil || value.OperationID != request.ID || value.EnvironmentID != env || value.Receipt.Validate(request) != nil {
			return nil, managedruntime.ErrConflict
		}
		r := value.Receipt
		s := sessions[r.WriteID]
		if s == nil {
			s = &session{view: managedruntime.ActiveWrite{WriteID: r.WriteID, EnvironmentID: env, Path: r.Path, Mode: r.Mode}, chunks: map[int]int64{}}
			sessions[r.WriteID] = s
		}
		if expiry := r.At.Add(2 * time.Hour); expiry.After(s.view.ExpiresAt) {
			s.view.ExpiresAt = expiry
		}
		switch r.Action {
		case "chunk":
			s.chunks[r.Index] = r.Bytes
		case "committed", "aborted":
			s.terminal = true
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var result []managedruntime.ActiveWrite
	for _, s := range sessions {
		if s.terminal || !s.view.ExpiresAt.After(time.Now()) {
			continue
		}
		for index, size := range s.chunks {
			s.view.Indices = append(s.view.Indices, index)
			s.view.Bytes += size
		}
		slices.Sort(s.view.Indices)
		result = append(result, s.view)
	}
	slices.SortFunc(result, func(a, b managedruntime.ActiveWrite) int {
		if a.WriteID < b.WriteID {
			return -1
		}
		if a.WriteID > b.WriteID {
			return 1
		}
		return 0
	})
	return result, nil
}
