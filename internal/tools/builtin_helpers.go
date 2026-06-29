package tools

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	case nil:
		return 0, false
	}
	return 0, false
}

func positiveInt(v any) (int, bool) {
	n, ok := toInt(v)
	return n, ok && n > 0
}
