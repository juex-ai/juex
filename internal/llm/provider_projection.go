package llm

import "encoding/json"

type providerProjectionOptions struct {
	// Codex Responses uses store=false. Replaying reasoning item IDs in that
	// mode makes the backend look up non-persisted items.
	OmitReasoning bool
}

func projectProviderTranscript(history []Message, profile ProviderProfile, opts providerProjectionOptions) []Message {
	filtered := make([]Message, 0, len(history))
	for _, m := range history {
		projected := m
		projected.Blocks = make([]Block, 0, len(m.Blocks))
		for _, b := range m.Blocks {
			if shouldProjectProviderBlock(b, profile, opts) {
				projected.Blocks = append(projected.Blocks, b)
			}
		}
		filtered = append(filtered, projected)
	}
	return compactHistoryForProvider(filtered)
}

func shouldProjectProviderBlock(b Block, profile ProviderProfile, opts providerProjectionOptions) bool {
	switch b.Type {
	case BlockText:
		return true
	case BlockToolUse, BlockToolResult:
		return profile.Capabilities.Tools
	case BlockReasoning:
		return !opts.OmitReasoning && profile.Capabilities.ReasoningReplay
	default:
		return false
	}
}

func normalizedFunctionParameters(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema)+2)
	for k, v := range schema {
		out[k] = v
	}
	if out["type"] == nil || out["type"] == "" {
		out["type"] = "object"
	}
	if out["properties"] == nil {
		out["properties"] = map[string]any{}
	}
	return out
}

func normalizedFunctionProperties(schema map[string]any) map[string]any {
	normalized := normalizedFunctionParameters(schema)
	props, ok := normalized["properties"].(map[string]any)
	if !ok || props == nil {
		return map[string]any{}
	}
	return props
}

func normalizedFunctionRequired(schema map[string]any) []string {
	normalized := normalizedFunctionParameters(schema)
	switch req := normalized["required"].(type) {
	case []string:
		return append([]string(nil), req...)
	case []any:
		out := make([]string, 0, len(req))
		for _, r := range req {
			if s, ok := r.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func toolCallArguments(input map[string]any) string {
	if input == nil {
		return "{}"
	}
	argBytes, err := json.Marshal(input)
	if err != nil {
		return "{}"
	}
	return string(argBytes)
}

func parseToolArguments(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	rawBytes := []byte(raw)
	var input map[string]any
	if err := json.Unmarshal(rawBytes, &input); err == nil {
		return input
	}
	var encoded string
	if err := json.Unmarshal(rawBytes, &encoded); err == nil {
		if err := json.Unmarshal([]byte(encoded), &input); err == nil {
			return input
		}
		return map[string]any{"_raw_arguments": encoded}
	}
	return map[string]any{"_raw_arguments": raw}
}
