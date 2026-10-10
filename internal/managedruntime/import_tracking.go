package managedruntime

import "github.com/juex-ai/juex/internal/foundation/llm"

func validateImportedTracking(value *ImportedInputTracking, messages map[string]llm.Message, inputs map[string]ImportedInput, sequence int64) error {
	if value == nil {
		return nil
	}
	if !importUUID(value.ScopeID) {
		return ErrInvalid
	}
	seen, associated := map[string]bool{}, map[string]bool{}
	current := 0
	previousOrder := int64(0)
	for _, item := range value.Entries {
		if !importUUID(item.InputID) || !importUUID(item.ScopeID) || seen[item.InputID] || item.AcceptedOrder <= previousOrder || item.AcceptedOrder > sequence {
			return ErrInvalid
		}
		seen[item.InputID] = true
		previousOrder = item.AcceptedOrder
		if item.Delivery != "registered" && item.Delivery != "delivered" && item.Delivery != "blocked" {
			return ErrInvalid
		}
		if item.MessageID != "" {
			message, ok := messages[item.MessageID]
			if !ok || associated[item.MessageID] || message.Role != llm.RoleUser || message.Kind != "" && message.Kind != llm.MessageKindDirect || item.Delivery == "delivered" && message.PolicyBlocked {
				return ErrInvalid
			}
			associated[item.MessageID] = true
		} else if item.Delivery == "delivered" {
			return ErrInvalid
		}
		if item.CheckedAt == nil {
			if item.CheckActionID != "" || item.CheckMessageID != "" || item.ToolUseID != "" {
				return ErrInvalid
			}
			if item.ScopeID == value.ScopeID {
				if _, ok := inputs[item.InputID]; !ok {
					return ErrInvalid
				}
				current++
			}
		} else {
			message, ok := messages[item.CheckMessageID]
			if item.CheckedAt.IsZero() || !importUUID(item.CheckActionID) || !ok || message.Role != llm.RoleAssistant || item.ToolUseID == "" || item.Delivery != "delivered" {
				return ErrInvalid
			}
			found := false
			for _, call := range message.ToolCalls() {
				if call.ToolUseID == item.ToolUseID && call.ToolName == "check_inputs" {
					found = true
				}
			}
			if !found {
				return ErrInvalid
			}
		}
	}
	if current > MaxTrackedInputs {
		return ErrInvalid
	}
	return nil
}
