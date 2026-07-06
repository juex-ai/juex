package observable

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/juex-ai/juex/internal/tools"
)

const (
	defaultObservationToolLimit = 20
	maxObservationToolLimit     = 100
)

func RegisterTools(reg *tools.Registry, manager *Manager) error {
	if reg == nil || manager == nil {
		return nil
	}
	for _, tool := range observableTools(manager) {
		if err := reg.Register(tool); err != nil {
			return err
		}
	}
	return nil
}

func observableTools(manager *Manager) []tools.Tool {
	idSchema := map[string]any{
		"type":       "object",
		"required":   []any{"id"},
		"properties": map[string]any{"id": map[string]any{"type": "string"}},
	}
	return []tools.Tool{
		{
			Name:        "observable_list",
			Description: "List configured JueX Observables and their runtime status. Call this before creating a new Observable to avoid duplicates.",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}},
			Handler: func(ctx context.Context, in map[string]any) (string, error) {
				_ = ctx
				_ = in
				return jsonString(manager.Status())
			},
		},
		{
			Name:        "observable_create",
			Description: "Create a workspace-local Observable, write .juex/observables.json, and start it immediately. Call observable_list before creating; avoid duplicate or near-duplicate Observables. Prefer JSONL parsers for structured event commands and filters for high-volume commands. Stopping is temporary. Deleting is permanent.",
			Schema:      specSchema(),
			Handler: func(ctx context.Context, in map[string]any) (string, error) {
				var spec Spec
				body, err := json.Marshal(in)
				if err != nil {
					return "", err
				}
				if err := json.Unmarshal(body, &spec); err != nil {
					return "", err
				}
				status, err := manager.Create(ctx, spec)
				if err != nil {
					return "", err
				}
				return jsonString(status)
			},
		},
		{
			Name:        "observable_start",
			Description: "Start a stopped or exited Observable for the current JueX process. Runtime starts are temporary; the config still controls startup on the next process launch.",
			Schema:      idSchema,
			Handler: func(ctx context.Context, in map[string]any) (string, error) {
				id, err := requiredString(in, "id")
				if err != nil {
					return "", err
				}
				if err := manager.Start(ctx, id); err != nil {
					return "", err
				}
				status, err := manager.StatusByID(id)
				if err != nil {
					return "", err
				}
				return jsonString(status)
			},
		},
		{
			Name:        "observable_stop",
			Description: "Stop a running Observable for the current JueX process. This is temporary; it starts again on the next JueX process startup unless deleted.",
			Schema:      idSchema,
			Handler: func(ctx context.Context, in map[string]any) (string, error) {
				id, err := requiredString(in, "id")
				if err != nil {
					return "", err
				}
				if err := manager.Stop(ctx, id); err != nil {
					return "", err
				}
				status, err := manager.StatusByID(id)
				if err != nil {
					return "", err
				}
				return jsonString(status)
			},
		},
		{
			Name:        "observable_delete",
			Description: "Delete an Observable from .juex/observables.json and stop it if running. Deleting is permanent; use observable_stop for a temporary runtime stop.",
			Schema:      idSchema,
			Handler: func(ctx context.Context, in map[string]any) (string, error) {
				id, err := requiredString(in, "id")
				if err != nil {
					return "", err
				}
				if err := manager.Delete(ctx, id); err != nil {
					return "", err
				}
				return jsonString(map[string]any{"deleted": id})
			},
		},
		{
			Name:        "observable_observations",
			Description: "List recent durable Observations, optionally for one Observable id. Results are bounded and include truncation/artifact metadata.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"id":    map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
				},
			},
			Handler: func(ctx context.Context, in map[string]any) (string, error) {
				_ = ctx
				records, err := manager.Observations(ObservationFilter{
					ObservableID: optionalString(in, "id"),
					Limit:        boundedObservationLimit(in),
				})
				if err != nil {
					return "", err
				}
				return jsonString(map[string]any{"observations": records})
			},
		},
	}
}

func specSchema() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []any{"id", "command", "batch"},
		"properties": map[string]any{
			"id":      map[string]any{"type": "string"},
			"name":    map[string]any{"type": "string"},
			"command": map[string]any{"type": "string"},
			"args":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"cwd":     map[string]any{"type": "string"},
			"env":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"streams": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"stdout", "stderr"}}},
			"defaults": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"kind":     map[string]any{"type": "string"},
					"severity": map[string]any{"type": "string", "enum": []any{"info", "warning", "error", "critical"}},
				},
			},
			"parser": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"type":           map[string]any{"type": "string", "enum": []any{"text", "jsonl"}},
					"content_field":  map[string]any{"type": "string"},
					"kind_field":     map[string]any{"type": "string"},
					"severity_field": map[string]any{"type": "string"},
					"time_field":     map[string]any{"type": "string"},
				},
			},
			"filters": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"batch": map[string]any{
				"type":     "object",
				"required": []any{"interval_seconds", "max_chars"},
				"properties": map[string]any{
					"interval_seconds": map[string]any{"type": "integer", "minimum": MinBatchIntervalSeconds, "maximum": MaxBatchIntervalSeconds},
					"max_chars":        map[string]any{"type": "integer", "minimum": 1, "maximum": MaxBatchChars},
				},
			},
		},
	}
}

func jsonString(value any) (string, error) {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return "", err
	}
	return string(body), nil
}

func requiredString(in map[string]any, key string) (string, error) {
	value := strings.TrimSpace(optionalString(in, key))
	if value == "" {
		return "", fmt.Errorf("observable tool: %s is required", key)
	}
	return value, nil
}

func optionalString(in map[string]any, key string) string {
	value, ok := in[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}

func optionalInt(in map[string]any, key string) int {
	value, ok := in[key]
	if !ok || value == nil {
		return 0
	}
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	case string:
		i, _ := strconv.Atoi(v)
		return i
	default:
		return 0
	}
}

func boundedObservationLimit(in map[string]any) int {
	limit := optionalInt(in, "limit")
	if limit <= 0 {
		return defaultObservationToolLimit
	}
	if limit > maxObservationToolLimit {
		return maxObservationToolLimit
	}
	return limit
}
