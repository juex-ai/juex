package postgres

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/managedruntime"
)

// Tool call IDs may repeat between attempts, even within one Turn. Resolve
// receipt/result ownership from Runtime's operation records, never call names.
func enrichToolAttempts(ctx context.Context, tx pgx.Tx, thread string, events []managedruntime.Event) error {
	if err := enrichObservationOrigins(ctx, tx, thread, events); err != nil {
		return err
	}
	indices := map[string][]int{}
	ids := []string{}
	for i, event := range events {
		var data struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
		}
		if json.Unmarshal(event.Data, &data) != nil {
			continue
		}
		id := ""
		if strings.HasPrefix(event.Kind, "tool.") {
			id = data.ID
		} else if event.Kind == "message.appended" && data.Kind == "tool_result" && strings.HasSuffix(data.ID, "-result") {
			id = strings.TrimSuffix(data.ID, "-result")
		}
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id {
			continue
		}
		if _, ok := indices[id]; !ok {
			ids = append(ids, id)
		}
		indices[id] = append(indices[id], i)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT j.id,j.attempt_id FROM runtime.tools j JOIN runtime.turns t ON t.id=j.turn_id WHERE j.id=ANY($1::uuid[]) AND t.thread_id=$2`, ids, thread)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, attempt string
		if err := rows.Scan(&id, &attempt); err != nil {
			return err
		}
		for _, i := range indices[id] {
			events[i].ToolAttemptID = attempt
		}
	}
	return rows.Err()
}

// Origin metadata is written by Runtime, never inferred from model-visible text.
// Both subscribed Inputs and Main's automatic observation batches use this path.
func enrichObservationOrigins(ctx context.Context, tx pgx.Tx, thread string, events []managedruntime.Event) error {
	indices := map[string][]int{}
	inputIDs := []string{}
	origins := make(map[int][]string)
	for i, event := range events {
		if event.Kind != "message.appended" {
			continue
		}
		var message struct {
			ID             string   `json:"id"`
			Kind           string   `json:"kind"`
			ObservationIDs []string `json:"observation_ids"`
		}
		if json.Unmarshal(event.Data, &message) != nil || message.Kind != "system_notice" {
			continue
		}
		if len(message.ObservationIDs) <= 50 {
			for _, id := range message.ObservationIDs {
				if parsed, err := uuid.Parse(id); err == nil && parsed.String() == id {
					origins[i] = append(origins[i], id)
				}
			}
		}
		if uuid.Validate(message.ID) != nil {
			continue
		}
		if _, ok := indices[message.ID]; !ok {
			inputIDs = append(inputIDs, message.ID)
		}
		indices[message.ID] = append(indices[message.ID], i)
	}
	if len(inputIDs) > 0 {
		rows, err := tx.Query(ctx, `SELECT id,source->>'observation_id' FROM runtime.inputs WHERE id=ANY($1::uuid[]) AND thread_id=$2 AND source->>'kind'='observation'`, inputIDs, thread)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			var observation *string
			if err := rows.Scan(&id, &observation); err != nil {
				rows.Close()
				return err
			}
			if observation != nil && uuid.Validate(*observation) == nil {
				for _, index := range indices[id] {
					origins[index] = append(origins[index], *observation)
				}
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	ids := []string{}
	for _, values := range origins {
		ids = append(ids, values...)
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT o.event_id FROM runtime.observations o JOIN runtime.threads t ON t.agent_id=o.agent_id WHERE t.id=$1 AND o.event_id=ANY($2::uuid[])`, thread, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	owned := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return err
		}
		owned[id] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for index, values := range origins {
		seen := map[string]bool{}
		for _, id := range values {
			if owned[id] && !seen[id] {
				events[index].ObservationIDs = append(events[index].ObservationIDs, id)
				seen[id] = true
			}
		}
	}
	return nil
}
