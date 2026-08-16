package tools

import "reflect"

func cloneSchemaMap(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	return cloneSchemaValue(reflect.ValueOf(schema)).Interface().(map[string]any)
}

func cloneSchemaValue(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.New(value.Type()).Elem()
		cloned.Set(cloneSchemaValue(value.Elem()))
		return cloned
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			cloned.SetMapIndex(iter.Key(), cloneSchemaValue(iter.Value()))
		}
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(cloneSchemaValue(value.Index(i)))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(cloneSchemaValue(value.Index(i)))
		}
		return cloned
	default:
		return value
	}
}

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
