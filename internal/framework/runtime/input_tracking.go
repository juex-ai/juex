package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/thread"
)

const InputCheckedType = "input.checked"

// The checklist has a separate bound from the live delivery queue.
const maxTrackedInputs = 256

type InputCheckedPayload struct {
	InputIDs  []string  `json:"input_ids"`
	ScopeID   string    `json:"scope_id"`
	CheckedAt time.Time `json:"checked_at"`
	ToolUseID string    `json:"tool_use_id"`
	MessageID string    `json:"message_id"`
}

func (q *PendingInputQueue) currentInputScopeID() string {
	if q.thread == nil {
		return thread.InitialGeneration
	}
	return q.thread.ContextScopeID()
}

func (q *PendingInputQueue) trackInputLocked(record *PendingInputRecord) {
	if q.trackUserInputs && llm.ClassifyUserMessage(record.Message).Kind == llm.MessageKindDirect {
		record.ScopeID = q.scopeID
		record.ExpiresAt = time.Time{}
	}
}

func (q *PendingInputQueue) processedMessageLocked(record PendingInputRecord) *llm.Message {
	if record.ScopeID == "" {
		return nil
	}
	if q.thread != nil {
		_, history := q.thread.Snapshot()
		for _, message := range history {
			if message.ID == record.MessageID {
				return &message
			}
		}
	}
	return record.ModelMessage
}

func (q *PendingInputQueue) refreshInputScopeLocked() error {
	if current := q.currentInputScopeID(); q.scopeID != current {
		q.scopeID = current
		q.checked = map[string]InputCheckedPayload{}
		if q.pruneInputScopesLocked() {
			return q.persistCurrentLocked()
		}
	}
	return nil
}

func (q *PendingInputQueue) pruneInputScopesLocked() bool {
	changed := false
	for _, id := range append([]string(nil), q.order...) {
		record := q.records[id]
		if record.ScopeID == "" || record.ScopeID == q.scopeID {
			continue
		}
		if record.State == PendingInputStateSettled {
			delete(q.records, id)
			q.order = removePendingInputID(q.order, id)
			changed = true
		} else if record.ModelMessage == nil {
			// Inputs still awaiting delivery belong to the scope that receives them.
			record.ScopeID = q.scopeID
			q.records[id] = record
			changed = true
		}
	}
	return changed
}

func (q *PendingInputQueue) uncheckedRecords() ([]PendingInputRecord, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return nil, err
	}
	var result []PendingInputRecord
	for _, id := range q.order {
		record := q.records[id]
		if record.ScopeID != "" && record.ScopeID == q.scopeID && record.CheckedAt == nil &&
			record.ProcessedAt != nil && record.ModelMessage != nil && !record.ModelMessage.PolicyBlocked {
			result = append(result, record)
		}
	}
	return result, nil
}

func (q *PendingInputQueue) checkInputs(ctx context.Context, ids []string, turnID string) ([]string, events.Event, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ensureLoadedLocked(); err != nil {
		return nil, events.Event{}, err
	}
	if q.thread == nil || len(ids) == 0 || len(ids) > maxTrackedInputs {
		return nil, events.Event{}, fmt.Errorf("input tracking: a Thread and 1..%d input IDs are required", maxTrackedInputs)
	}
	seen := map[string]bool{}
	confirmed := make([]string, 0, len(ids))
	var pending []string
	for _, id := range ids {
		if id == "" || strings.TrimSpace(id) != id {
			return nil, events.Event{}, fmt.Errorf("input tracking: invalid input ID %q", id)
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if previous, ok := q.checked[id]; ok && previous.ScopeID == q.scopeID {
			confirmed = append(confirmed, id)
			continue
		}
		record, ok := q.records[id]
		if !ok || record.ScopeID == "" || record.ScopeID != q.scopeID || record.ProcessedAt == nil ||
			record.ModelMessage == nil || record.ModelMessage.PolicyBlocked {
			return nil, events.Event{}, fmt.Errorf("input tracking: input %q is unknown, undelivered, or outside this work scope", id)
		}
		confirmed = append(confirmed, id)
		if record.CheckedAt == nil {
			pending = append(pending, id)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, events.Event{}, err
	}
	if len(pending) == 0 {
		return confirmed, events.Event{}, nil
	}
	call := toolcore.ToolCallEventsFromContext(ctx)
	payload := InputCheckedPayload{InputIDs: pending, ScopeID: q.scopeID, CheckedAt: q.nowMillis(), ToolUseID: call.ToolUseID, MessageID: call.MessageID}
	event := events.Normalize(events.Event{Type: InputCheckedType, TurnID: turnID, Payload: payload})
	// No synchronous subscribers run under the queue lock. A later state-write
	// failure is reconciled from this committed fact on the next read.
	if err := q.thread.AppendEvent(event); err != nil {
		q.loaded = false
		return nil, events.Event{}, err
	}
	q.applyInputCheckLocked(payload)
	if err := q.persistCurrentLocked(); err != nil {
		return nil, events.Event{}, err
	}
	return confirmed, event, nil
}

func (q *PendingInputQueue) applyInputCheckLocked(payload InputCheckedPayload) bool {
	if payload.ScopeID == "" || payload.ScopeID != q.scopeID {
		return false
	}
	changed := false
	for _, id := range payload.InputIDs {
		q.checked[id] = payload
		record, ok := q.records[id]
		if !ok || record.ScopeID != payload.ScopeID || record.CheckedAt != nil {
			continue
		}
		record.CheckedAt = &payload.CheckedAt
		if record.State == PendingInputStateSettled {
			delete(q.records, id)
			q.order = removePendingInputID(q.order, id)
		} else {
			q.records[id] = record
		}
		changed = true
	}
	return changed
}

func (e *Engine) UncheckedInputs(ctx context.Context) ([]runtimemodule.InputReminder, error) {
	if e == nil || !e.TrackUserInputs {
		return nil, nil
	}
	queue := e.currentPendingInputQueue()
	if queue == nil {
		return nil, nil
	}
	records, err := queue.uncheckedRecords()
	if err != nil {
		return nil, err
	}
	result := make([]runtimemodule.InputReminder, 0, len(records))
	for _, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		message, _, err := e.projectMessageLocked(*record.ModelMessage, effectiveCompactionPolicy(e.Compaction, e.ContextWindow))
		if err != nil {
			return nil, err
		}
		message, err = e.renderProjectedArtifactReadURIs(message)
		if err != nil {
			return nil, err
		}
		// JSON preserves every block and attachment reference without a second
		// model-generated summary or a new input-reading protocol.
		content, err := json.Marshal(message.Blocks)
		if err != nil {
			return nil, err
		}
		result = append(result, runtimemodule.InputReminder{ID: record.ID, Content: string(content)})
	}
	return result, nil
}

func (e *Engine) CheckInputs(ctx context.Context, ids []string) ([]string, error) {
	if e == nil || !e.TrackUserInputs {
		return nil, fmt.Errorf("input tracking is disabled")
	}
	call := toolcore.ToolCallEventsFromContext(ctx)
	if call.MessageID != "" {
		_, history := e.currentThread().Snapshot()
		for _, message := range history {
			if message.ID != call.MessageID {
				continue
			}
			for _, other := range message.ToolCalls() {
				if other.ToolName != call.Name {
					return nil, fmt.Errorf("input tracking: finish other tools before checking; call the check tool in a subsequent response")
				}
			}
		}
	}
	queue := e.currentPendingInputQueue()
	if queue == nil {
		return nil, fmt.Errorf("input tracking: Thread unavailable")
	}
	confirmed, event, err := queue.checkInputs(ctx, ids, e.PendingInputStatus().TurnID)
	if err != nil {
		return nil, err
	}
	if event.Type != "" && e.Bus != nil {
		e.Bus.PublishCommitted(event)
	}
	return confirmed, nil
}

func (e *Engine) checkInputScopeRenewal() error {
	queue := e.currentPendingInputQueue()
	if queue == nil {
		return nil
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if err := queue.ensureLoadedLocked(); err != nil {
		return err
	}
	for _, record := range queue.records {
		if record.ScopeID != "" && record.ScopeID == queue.scopeID && record.CheckedAt == nil {
			return fmt.Errorf("input tracking: unchecked inputs remain; use context_compact to continue, or let the user start /new")
		}
	}
	return nil
}
