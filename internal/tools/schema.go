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

func schemaWithReservedTimeout(schema map[string]any) map[string]any {
	out := normalizeInputSchema(schema)
	props, ok := out["properties"].(map[string]any)
	if !ok {
		props = map[string]any{}
		out["properties"] = props
	}
	if _, ok := props["timeout"]; !ok {
		props["timeout"] = map[string]any{
			"type":        "integer",
			"description": "Seconds to allow this tool call to run. Defaults to 60 and is capped at 300.",
			"minimum":     1,
			"maximum":     MaxTimeoutSeconds,
		}
	}
	return out
}

func schemaDeclaresProperty(schema map[string]any, name string) bool {
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		return false
	}
	_, ok = props[name]
	return ok
}

func normalizeSchemaValue(value any, key, parentKey string) any {
	switch v := value.(type) {
	case nil:
		if key == "enum" {
			return nil
		}
		if parentKey == "properties" || parentKey == "patternProperties" || key == "items" || key == "contains" || key == "not" {
			return map[string]any{}
		}
		return nil
	case map[string]any:
		out := make(map[string]any, len(v))
		for childKey, child := range v {
			normalized := normalizeSchemaValue(child, childKey, key)
			if normalized == nil {
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
