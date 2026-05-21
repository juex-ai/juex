package tools

func normalizeInputSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return map[string]any{"type": "object"}
	}
	normalized, ok := normalizeSchemaValue(schema, "", "").(map[string]any)
	if !ok || normalized == nil {
		return map[string]any{"type": "object"}
	}
	return normalized
}

func normalizeSchemaValue(value any, key, parentKey string) any {
	switch v := value.(type) {
	case nil:
		if key == "enum" {
			return nil
		}
		if parentKey == "properties" || key == "items" || key == "contains" || key == "not" {
			return map[string]any{}
		}
		return nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for childKey, child := range v {
			if child == nil && childKey == "additionalProperties" {
				continue
			}
			normalized := normalizeSchemaValue(child, childKey, key)
			if normalized == nil && childKey != "enum" {
				continue
			}
			out[childKey] = normalized
		}
		return out
	case []any:
		out := make([]any, 0, len(v))
		for _, child := range v {
			if child == nil && key == "enum" {
				out = append(out, nil)
				continue
			}
			normalized := normalizeSchemaValue(child, "", key)
			if normalized == nil {
				normalized = map[string]any{}
			}
			out = append(out, normalized)
		}
		return out
	default:
		return value
	}
}
