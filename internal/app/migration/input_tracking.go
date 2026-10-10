package migration

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

type sourceTrackedInput struct {
	input, message, scope, delivery string
	order                           int64
	queueOrder                      int
	checked                         *sourceInputCheck
}
type sourceInputCheck struct {
	InputIDs  []string  `json:"input_ids"`
	ScopeID   string    `json:"scope_id"`
	CheckedAt time.Time `json:"checked_at"`
	ToolUseID string    `json:"tool_use_id"`
	MessageID string    `json:"message_id"`
}
type sourceTracking struct {
	scope   string
	entries []sourceTrackedInput
}

// Queue rows alone are incomplete: the source pruned settled checked inputs
// and could crash after committing a check fact but before rewriting its queue.
func sourceInputTracking(thread legacy.Thread) (sourceTracking, error) {
	result := sourceTracking{}
	rows := map[string]*sourceTrackedInput{}
	messages := map[string]llm.Message{}
	messageOrder := map[string]int64{}
	position := int64(0)
	validScopes := map[string]bool{}
	// Early source journals can retain a completed direct Turn before the
	// input.tracked event existed. Recover only its uniquely proven association
	// when a later committed check explicitly names that input.
	type deliveredTurn struct {
		message, scope, input                 string
		order                                 int64
		admitted, started, completed, invalid bool
	}
	turns := map[string]*deliveredTurn{}
	delivered := map[string]*sourceTrackedInput{}
	ambiguous := map[string]bool{}
	recovered := map[string]bool{}
	deliveredMessages := map[string]string{}
	for _, commit := range thread.Commits {
		for _, fact := range commit.Facts {
			position++
			candidates := []*llm.Message{fact.Message, fact.Summary}
			if fact.Seed != nil {
				for i := range fact.Seed.ProviderMessages {
					candidates = append(candidates, &fact.Seed.ProviderMessages[i])
				}
			}
			for _, m := range candidates {
				if m != nil {
					if old, ok := messages[m.ID]; ok && !reflect.DeepEqual(old, *m) {
						return result, errors.New("tracking message has conflicting history")
					}
					messages[m.ID] = *m
					if messageOrder[m.ID] == 0 {
						messageOrder[m.ID] = position
					}
				}
			}
		}
	}
	position = 0
	for _, commit := range thread.Commits {
		for _, fact := range commit.Facts {
			position++
			switch fact.Type {
			case "thread.created":
				result.scope = "g000001"
				validScopes[result.scope] = true
			case "context.renewed", "context.compacted":
				if fact.Seed == nil || fact.Seed.ContextScopeID == "" {
					return result, errors.New("tracking requires a proven context scope")
				}
				if fact.Type == "context.compacted" && fact.Seed.ContextScopeID != result.scope {
					return result, errors.New("compaction changed input scope")
				}
				if fact.Type == "context.renewed" && (fact.Seed.ContextScopeID != commit.GenerationID || validScopes[fact.Seed.ContextScopeID]) {
					return result, errors.New("invalid renewed input scope")
				}
				result.scope = fact.Seed.ContextScopeID
				validScopes[result.scope] = true
			case "event.recorded":
				var event struct {
					Type    string          `json:"type"`
					TurnID  string          `json:"turn_id"`
					Payload json.RawMessage `json:"payload"`
				}
				if json.Unmarshal(fact.Event, &event) != nil {
					return result, errors.New("invalid tracking event")
				}
				switch event.Type {
				case "turn.admitted", "turn.started", "turn.completed", "turn.cancelled", "turn.errored":
					if event.TurnID == "" {
						continue
					}
					var p struct {
						MessageID string   `json:"message_id"`
						Kind      string   `json:"kind"`
						InputIDs  []string `json:"input_ids"`
					}
					turn := turns[event.TurnID]
					if turn == nil {
						turn = &deliveredTurn{}
						turns[event.TurnID] = turn
					}
					if turn.completed && turn.input != "" {
						ambiguous[turn.input] = true
					}
					if json.Unmarshal(event.Payload, &p) != nil {
						turn.invalid = true
						continue
					}
					switch event.Type {
					case "turn.admitted":
						if turn.admitted || turn.started || p.MessageID == "" {
							turn.invalid = true
						}
						turn.admitted = true
						turn.message = p.MessageID
						turn.scope = result.scope
					case "turn.started":
						m, ok := messages[p.MessageID]
						if !turn.admitted || turn.started || turn.message != p.MessageID || turn.scope != result.scope || p.Kind != "direct" || !ok || messageOrder[p.MessageID] > position || m.Role != llm.RoleUser || m.Kind != llm.MessageKindDirect || m.PolicyBlocked {
							turn.invalid = true
						}
						turn.started = true
						turn.order = position
					case "turn.cancelled", "turn.errored":
						turn.invalid = true
						for _, id := range p.InputIDs {
							if delivered[id] != nil {
								ambiguous[id] = true
							}
						}
					case "turn.completed":
						if turn.completed {
							turn.invalid = true
						}
						turn.completed = true
						if turn.invalid || !turn.started || turn.scope != result.scope || len(p.InputIDs) != 1 {
							continue
						}
						id := p.InputIDs[0]
						if id == "" || strings.TrimSpace(id) != id {
							continue
						}
						if delivered[id] != nil {
							ambiguous[id] = true
							continue
						}
						if other := deliveredMessages[turn.message]; other != "" && other != id {
							ambiguous[other] = true
							ambiguous[id] = true
						}
						deliveredMessages[turn.message] = id
						turn.input = id
						delivered[id] = &sourceTrackedInput{input: id, message: turn.message, scope: turn.scope, delivery: "delivered", order: turn.order}
					}
				case "input.tracked":
					var p struct {
						InputID   string `json:"input_id"`
						MessageID string `json:"message_id"`
						ScopeID   string `json:"scope_id"`
					}
					if json.Unmarshal(event.Payload, &p) != nil || p.InputID == "" || strings.TrimSpace(p.InputID) != p.InputID || p.ScopeID != result.scope || p.MessageID == "" || rows[p.InputID] != nil {
						return result, errors.New("invalid or duplicate input association")
					}
					m, ok := messages[p.MessageID]
					if !ok || messageOrder[p.MessageID] > position || m.Role != llm.RoleUser || m.Kind != "" && m.Kind != llm.MessageKindDirect || m.PolicyBlocked {
						return result, errors.New("tracked input lacks a delivered user message")
					}
					rows[p.InputID] = &sourceTrackedInput{input: p.InputID, message: p.MessageID, scope: p.ScopeID, delivery: "delivered", order: position}
				case "input.checked":
					var p sourceInputCheck
					if json.Unmarshal(event.Payload, &p) != nil || p.ScopeID != result.scope || p.CheckedAt.IsZero() || p.ToolUseID == "" || p.MessageID == "" || len(p.InputIDs) < 1 || len(p.InputIDs) > managedruntime.MaxTrackedInputs {
						return result, errors.New("invalid input check evidence")
					}
					m, ok := messages[p.MessageID]
					if !ok || messageOrder[p.MessageID] > position || m.Role != llm.RoleAssistant {
						return result, errors.New("check lacks original assistant message")
					}
					found := false
					requested := map[string]bool{}
					for _, call := range m.ToolCalls() {
						if call.ToolName != "check_inputs" {
							return result, errors.New("check combined with another tool")
						}
						if call.ToolUseID == p.ToolUseID {
							if found {
								return result, errors.New("check tool identity is ambiguous")
							}
							found = true
							raw, err := json.Marshal(call.Input["input_ids"])
							var ids []string
							if err != nil || json.Unmarshal(raw, &ids) != nil || len(ids) < 1 || len(ids) > managedruntime.MaxTrackedInputs {
								return result, errors.New("check tool has invalid input IDs")
							}
							for _, id := range ids {
								requested[id] = true
							}
						}
					}
					if !found {
						return result, errors.New("check lacks original tool call")
					}
					pending := 0
					for id := range requested {
						row := rows[id]
						if row == nil && delivered[id] != nil && !ambiguous[id] {
							candidate := delivered[id]
							if candidate.scope == p.ScopeID {
								for _, prior := range rows {
									if prior.message == candidate.message {
										return result, errors.New("completed Turn message belongs to another input")
									}
								}
								copy := *candidate
								row = &copy
								rows[id] = row
								recovered[id] = true
							}
						}
						if row == nil || row.scope != p.ScopeID || row.delivery != "delivered" {
							return result, errors.New("check parameters could not atomically succeed")
						}
						if row.checked == nil {
							pending++
						}
					}
					if pending != len(p.InputIDs) {
						return result, errors.New("check event omits or adds unchecked parameters")
					}
					seen := map[string]bool{}
					for _, id := range p.InputIDs {
						row := rows[id]
						if !requested[id] || seen[id] || row == nil || row.scope != p.ScopeID || row.checked != nil {
							return result, errors.New("check names unassociated or already checked input")
						}
						seen[id] = true
						copy := p
						row.checked = &copy
					}
				case "input.scope_changed":
					var p struct {
						ScopeID string `json:"scope_id"`
					}
					if json.Unmarshal(event.Payload, &p) != nil || p.ScopeID != result.scope {
						return result, errors.New("input scope event contradicts boundary")
					}
				}
			}
		}
	}
	for id := range recovered {
		if ambiguous[id] {
			return result, errors.New("checked input has contradictory completed Turn associations")
		}
	}
	if result.scope == "" || thread.ContextScopeID != result.scope {
		return result, errors.New("current input scope contradicts history")
	}
	queue := map[string]bool{}
	for index, input := range thread.Inputs {
		if input.ScopeID == "" {
			continue
		}
		if !validScopes[input.ScopeID] || queue[input.ID] || input.Message.Role != llm.RoleUser || input.Message.Kind != "" && input.Message.Kind != llm.MessageKindDirect {
			return result, errors.New("invalid tracked queue input")
		}
		queue[input.ID] = true
		row := rows[input.ID]
		if row == nil {
			if input.CheckedAt != nil {
				return result, errors.New("queue check lacks committed evidence")
			}
			if input.ModelMessage != nil && !input.ModelMessage.PolicyBlocked {
				return result, errors.New("delivered queue input lacks committed association")
			}
			position++
			row = &sourceTrackedInput{input: input.ID, scope: input.ScopeID, delivery: "registered", order: position}
			rows[input.ID] = row
			if input.ModelMessage != nil && input.ModelMessage.PolicyBlocked {
				m, ok := messages[input.MessageID]
				if !ok || !reflect.DeepEqual(m, *input.ModelMessage) {
					return result, errors.New("blocked queue message differs from history")
				}
				row.delivery = "blocked"
				row.message = input.MessageID
			}
		} else {
			if row.message != input.MessageID || row.scope != input.ScopeID {
				return result, errors.New("queue association contradicts history")
			}
			// The committed association proves delivery even if queue persistence
			// stopped between the message commit and its acknowledgement.
			if input.ModelMessage != nil && !reflect.DeepEqual(messages[row.message], *input.ModelMessage) {
				return result, errors.New("queue model message contradicts history")
			}
			if input.CheckedAt != nil && (row.checked == nil || !input.CheckedAt.Equal(row.checked.CheckedAt)) {
				return result, errors.New("queue check contradicts committed evidence")
			}
		}
		row.queueOrder = index + 1
	}
	current := 0
	for id, row := range rows {
		if row.scope == result.scope && row.checked == nil {
			if !queue[id] {
				return result, errors.New("current unchecked input missing from queue")
			}
			current++
		}
		result.entries = append(result.entries, *row)
	}
	if current > managedruntime.MaxTrackedInputs {
		return result, errors.New("source input checklist exceeds capacity")
	}
	// Remaining queue order is the source's accepted priority, even when a
	// retry delivered an earlier input later. Pruned historical rows only have
	// fact order; never fabricate their original acceptance timestamps.
	sort.Slice(result.entries, func(i, j int) bool {
		left, right := result.entries[i], result.entries[j]
		if left.queueOrder == 0 && right.queueOrder == 0 {
			return left.order < right.order
		}
		return left.queueOrder < right.queueOrder
	})
	return result, nil
}

func (c *threadConverter) convertInputTracking(source sourceTracking) *managedruntime.ImportedInputTracking {
	namespace := c.messages.threadID(c.originalID)
	result := &managedruntime.ImportedInputTracking{ScopeID: mappedIdentity(namespace, "input-scope", source.scope).String(), Entries: []managedruntime.InputCheck{}}
	for index, item := range source.entries {
		// Compress the proven source order into the imported event range. Facts
		// and Runtime events have different cardinality; live admission uses the
		// latter's sequence and must always follow every imported checklist row.
		value := managedruntime.InputCheck{InputID: mappedIdentity(namespace, "input", item.input).String(), ScopeID: mappedIdentity(namespace, "input-scope", item.scope).String(), MessageID: c.messages.messageID(c.originalID, item.message), AcceptedOrder: int64(index + 1), Delivery: item.delivery}
		c.identities.Inputs[c.originalID][item.input] = value.InputID
		if check := item.checked; check != nil {
			at := check.CheckedAt
			value.CheckedAt = &at
			value.CheckActionID = mappedIdentity(namespace, "input-check", fmt.Sprintf("%d:%s%s", len(check.MessageID), check.MessageID, check.ToolUseID)).String()
			value.CheckMessageID = c.messages.messageID(c.originalID, check.MessageID)
			value.ToolUseID = check.ToolUseID
		}
		result.Entries = append(result.Entries, value)
	}
	return result
}
