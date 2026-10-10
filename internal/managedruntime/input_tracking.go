package managedruntime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/llm"
)

const MaxTrackedInputs = 256

// Input checks are model judgements, independent of Input execution outcomes.
// Scope survives compaction; an explicit new context ends it without checking.
type InputCheck struct {
	InputID        string     `json:"input_id"`
	ScopeID        string     `json:"scope_id"`
	MessageID      string     `json:"message_id,omitempty"`
	AcceptedOrder  int64      `json:"accepted_order"`
	Delivery       string     `json:"delivery"`
	CheckedAt      *time.Time `json:"checked_at,omitempty"`
	CheckActionID  string     `json:"check_action_id,omitempty"`
	CheckMessageID string     `json:"check_message_id,omitempty"`
	ToolUseID      string     `json:"tool_use_id,omitempty"`
}

type InputChecklist struct {
	Enabled bool         `json:"enabled"`
	ScopeID string       `json:"scope_id"`
	Items   []InputCheck `json:"items"`
}

type InputReminder struct {
	InputID string
	Message llm.Message
}

type InputCheckQuery struct {
	MessageIDs []string `json:"message_ids"`
}
type InputCheckPage struct {
	Enabled bool         `json:"enabled"`
	ScopeID string       `json:"scope_id"`
	Items   []InputCheck `json:"items"`
}

func (q InputCheckQuery) Validate() error {
	if len(q.MessageIDs) < 1 || len(q.MessageIDs) > 500 {
		return ErrInvalid
	}
	for _, id := range q.MessageIDs {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil || parsed.String() != id {
			return ErrInvalid
		}
	}
	return nil
}

type InputCheckStore interface {
	InputChecks(context.Context, Scope, string, InputCheckQuery) (InputCheckPage, error)
}

func (s *Service) InputChecks(ctx context.Context, actor, tenant, agent, thread string, query InputCheckQuery) (InputCheckPage, error) {
	if id, err := uuid.Parse(thread); err != nil || id == uuid.Nil || id.String() != thread || query.Validate() != nil {
		return InputCheckPage{}, ErrInvalid
	}
	scope, err := s.Authority.Authorize(ctx, actor, tenant, agent, false)
	if err != nil {
		return InputCheckPage{}, err
	}
	store, ok := s.Store.(InputCheckStore)
	if !ok {
		return InputCheckPage{}, ErrInvalid
	}
	return store.InputChecks(ctx, scope, thread, query)
}

// A fixed shared preview budget bounds prose without dropping any identity or
// original context reference. If those alone exceed model capacity, ordinary
// context admission holds/falls back instead of silently forgetting inputs.
func InputReminderContext(items []InputReminder, model ModelConfig) string {
	if len(items) == 0 {
		return ""
	}
	budget := min(8192, max(512, model.ContextWindow/8))
	header := "Current unchecked direct user inputs (authoritative Runtime checklist; model judgement, not proof of success). Call check_inputs only for fully handled requests or requirements completely recorded in durable Tasks after the other tools succeeded in a previous response. Keep waiting work and still-applicable constraints unchecked. Turn completion does not check inputs or trigger continuation. Read original text with read_context using the references below.\n"
	for preview := 256; ; preview /= 2 {
		var out strings.Builder
		out.WriteString(header)
		for _, item := range items {
			blocks := []map[string]any{}
			for index, block := range item.Message.Blocks {
				if block.Type == llm.BlockText {
					value := map[string]any{"reference": ContextReference(item.Message.ID, index, "text")}
					if preview > 0 {
						text := []rune(block.Text)
						value["preview"] = string(text[:min(preview, len(text))])
					}
					blocks = append(blocks, value)
				} else if block.Type == llm.BlockImage && block.Media != nil {
					blocks = append(blocks, map[string]any{"image": block.Media})
				}
			}
			encoded, _ := json.Marshal(map[string]any{"input_id": item.InputID, "message_id": item.Message.ID, "blocks": blocks})
			fmt.Fprintln(&out, string(encoded))
		}
		if preview == 0 || llm.EstimateTextTokens(out.String()) <= budget {
			return out.String()
		}
	}
}
