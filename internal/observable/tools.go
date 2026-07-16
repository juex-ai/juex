package observable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/juex-ai/juex/internal/tools"
)

const (
	defaultObservationToolLimit = 20
	maxObservationToolLimit     = 100
)

func ToolDefinitions() []tools.ToolDefinition {
	idSchema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"id"},
		"properties":           map[string]any{"id": map[string]any{"type": "string"}},
	}
	return []tools.ToolDefinition{
		{
			Name:        "observable_list",
			Group:       tools.ToolGroupObservable,
			Description: "List configured JueX Observables and their runtime status. Call this before creating a new Observable to avoid duplicates.",
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
		{
			Name:        "observable_create",
			Group:       tools.ToolGroupObservable,
			Description: "Create a workspace-local command Observable and start it immediately. Call observable_list first and avoid duplicates. The input is flat; batch defaults are safe if omitted. Use schedule_create for scheduled or recurring activation. Stopping is temporary; deleting is permanent.",
			Schema:      commandCreateSchema(),
		},
		{
			Name:        "schedule_create",
			Group:       tools.ToolGroupObservable,
			Description: "Create a workspace-local Schedule that emits a pre-authored Observation at scheduled times. Set exactly one of once, daily, or interval; daily requires timezone. Use this for scheduled or recurring activation instead of a polling script or command Observable. Call observable_list first and avoid duplicates.",
			Schema:      scheduleCreateSchema(),
		},
		{
			Name:        "observable_start",
			Group:       tools.ToolGroupObservable,
			Description: "Start a stopped or exited Observable for the current JueX process. Runtime starts are temporary; the config still controls startup on the next process launch.",
			Schema:      idSchema,
		},
		{
			Name:        "observable_stop",
			Group:       tools.ToolGroupObservable,
			Description: "Stop a running Observable for the current JueX process. This is temporary; it starts again on the next JueX process startup unless deleted.",
			Schema:      idSchema,
		},
		{
			Name:        "observable_delete",
			Group:       tools.ToolGroupObservable,
			Description: "Delete an Observable from .juex/observables.json and stop it if running. Deleting is permanent; use observable_stop for a temporary runtime stop.",
			Schema:      idSchema,
		},
		{
			Name:        "observable_observations",
			Group:       tools.ToolGroupObservable,
			Description: "List recent durable Observations, optionally for one Observable id. Results are bounded and include truncation/artifact metadata.",
			Schema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"id":    map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
				},
			},
		},
	}
}

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
	definitions := ToolDefinitions()
	return []tools.Tool{
		definitions[0].Bind(func(ctx context.Context, in map[string]any) (string, error) {
			_ = ctx
			_ = in
			return jsonString(manager.Status())
		}),
		definitions[1].Bind(func(ctx context.Context, in map[string]any) (string, error) {
			spec, err := commandSpecFromCreateInput(in)
			if err != nil {
				return "", err
			}
			status, err := manager.Create(ctx, spec)
			if err != nil {
				return "", err
			}
			return jsonString(status)
		}),
		definitions[2].Bind(func(ctx context.Context, in map[string]any) (string, error) {
			spec, err := scheduleSpecFromCreateInput(in)
			if err != nil {
				return "", err
			}
			status, err := manager.Create(ctx, spec)
			if err != nil {
				return "", err
			}
			return jsonString(status)
		}),
		definitions[3].Bind(func(ctx context.Context, in map[string]any) (string, error) {
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
		}),
		definitions[4].Bind(func(ctx context.Context, in map[string]any) (string, error) {
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
		}),
		definitions[5].Bind(func(ctx context.Context, in map[string]any) (string, error) {
			id, err := requiredString(in, "id")
			if err != nil {
				return "", err
			}
			if err := manager.Delete(ctx, id); err != nil {
				return "", err
			}
			return jsonString(map[string]any{"deleted": id})
		}),
		definitions[6].Bind(func(ctx context.Context, in map[string]any) (string, error) {
			_ = ctx
			records, err := manager.Observations(ObservationFilter{
				ObservableID: optionalString(in, "id"),
				Limit:        boundedObservationLimit(in),
			})
			if err != nil {
				return "", err
			}
			return jsonString(map[string]any{"observations": records})
		}),
	}
}

type commandCreateInput struct {
	ID          string                 `json:"id"`
	Name        string                 `json:"name,omitempty"`
	Command     string                 `json:"command"`
	Args        []string               `json:"args,omitempty"`
	CWD         string                 `json:"cwd,omitempty"`
	Env         map[string]string      `json:"env,omitempty"`
	Streams     []string               `json:"streams,omitempty"`
	Parser      *ParserSpec            `json:"parser,omitempty"`
	Filters     []FilterSpec           `json:"filters,omitempty"`
	Batch       BatchSpec              `json:"batch,omitempty"`
	OnExit      OnExitSpec             `json:"on_exit,omitempty"`
	Observation CommandObservationSpec `json:"observation,omitempty"`
}

type scheduleCreateInput struct {
	ID          string                  `json:"id"`
	Name        string                  `json:"name,omitempty"`
	Timezone    string                  `json:"timezone,omitempty"`
	Once        *OnceSchedule           `json:"once,omitempty"`
	Daily       *DailySchedule          `json:"daily,omitempty"`
	Interval    *IntervalSchedule       `json:"interval,omitempty"`
	CatchUp     CatchUpSpec             `json:"catch_up,omitempty"`
	Observation ScheduleObservationSpec `json:"observation"`
}

func commandSpecFromCreateInput(in map[string]any) (Spec, error) {
	if err := rejectCommandCreateRouting(in); err != nil {
		return Spec{}, err
	}
	input, err := decodeCreateInput[commandCreateInput](in)
	if err != nil {
		return Spec{}, fmt.Errorf("observable_create: %w", err)
	}
	return NewCommandSpec(input.ID, input.Name, CommandSourceSpec{
		Command:     input.Command,
		Args:        input.Args,
		CWD:         input.CWD,
		Env:         input.Env,
		Streams:     input.Streams,
		Parser:      input.Parser,
		Filters:     input.Filters,
		Batch:       input.Batch,
		OnExit:      input.OnExit,
		Observation: input.Observation,
	})
}

func scheduleSpecFromCreateInput(in map[string]any) (Spec, error) {
	input, err := decodeCreateInput[scheduleCreateInput](in)
	if err != nil {
		return Spec{}, fmt.Errorf("schedule_create: %w", err)
	}
	return NewScheduleSpec(input.ID, input.Name, ScheduleSourceSpec{
		Timezone:    input.Timezone,
		Once:        input.Once,
		Daily:       input.Daily,
		Interval:    input.Interval,
		CatchUp:     input.CatchUp,
		Observation: input.Observation,
	})
}

func rejectCommandCreateRouting(in map[string]any) error {
	commandRoute, scheduleRoute, err := legacyCreateRoutes(in)
	if err != nil {
		return err
	}
	flatSchedule := hasAnyCreateField(in, "timezone", "once", "daily", "interval", "catch_up")
	observation, ok := in["observation"].(map[string]any)
	if ok && hasAnyCreateField(observation, "content", "attachments") {
		flatSchedule = true
	}
	if commandRoute && (scheduleRoute || flatSchedule) {
		return fmt.Errorf("observable_create: mixed command and schedule source routing fields are not allowed")
	}
	if scheduleRoute || flatSchedule {
		return fmt.Errorf("observable_create creates command Observables; use schedule_create for scheduled Observations")
	}
	if commandRoute {
		return fmt.Errorf("observable_create expects flat command input; move nested command fields to the top level and remove source, type, or command_config")
	}
	return nil
}

func legacyCreateRoutes(in map[string]any) (command, schedule bool, err error) {
	if value, exists := in["type"]; exists {
		command, schedule, err = createRouteFromDiscriminator("type", value)
		if err != nil {
			return false, false, err
		}
	}
	if _, exists := in["command_config"]; exists {
		command = true
	}
	if _, exists := in["schedule_config"]; exists {
		schedule = true
	}
	value, exists := in["source"]
	if !exists {
		return command, schedule, nil
	}
	source, ok := value.(map[string]any)
	if !ok {
		return false, false, fmt.Errorf("observable_create: source must be an object")
	}
	sourceCommand := hasAnyCreateField(source, "command", "args", "cwd", "env", "streams", "parser", "filters", "batch", "on_exit")
	sourceSchedule := hasAnyCreateField(source, "timezone", "once", "daily", "interval", "catch_up")
	if discriminator, exists := source["type"]; exists {
		discriminatorCommand, discriminatorSchedule, discriminatorErr := createRouteFromDiscriminator("source.type", discriminator)
		if discriminatorErr != nil {
			return false, false, discriminatorErr
		}
		sourceCommand = sourceCommand || discriminatorCommand
		sourceSchedule = sourceSchedule || discriminatorSchedule
	}
	if !sourceCommand && !sourceSchedule {
		return false, false, fmt.Errorf("observable_create: source must identify command or schedule fields")
	}
	return command || sourceCommand, schedule || sourceSchedule, nil
}

func createRouteFromDiscriminator(field string, value any) (command, schedule bool, err error) {
	discriminator, ok := value.(string)
	if !ok {
		return false, false, fmt.Errorf("observable_create: unknown source discriminator %s=%v; expected command or schedule", field, value)
	}
	switch strings.TrimSpace(discriminator) {
	case SourceTypeCommand:
		return true, false, nil
	case SourceTypeSchedule:
		return false, true, nil
	default:
		return false, false, fmt.Errorf("observable_create: unknown source discriminator %s=%q; expected command or schedule", field, discriminator)
	}
}

func hasAnyCreateField(in map[string]any, fields ...string) bool {
	for _, field := range fields {
		if _, exists := in[field]; exists {
			return true
		}
	}
	return false
}

func decodeCreateInput[T any](in map[string]any) (T, error) {
	var input T
	body, err := json.Marshal(in)
	if err != nil {
		return input, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return input, fmt.Errorf("unexpected trailing input")
	}
	return input, nil
}

func commandCreateSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"id", "command"},
		"properties": map[string]any{
			"id":          map[string]any{"type": "string"},
			"name":        map[string]any{"type": "string"},
			"command":     map[string]any{"type": "string"},
			"args":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"cwd":         map[string]any{"type": "string"},
			"env":         map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
			"streams":     streamSchema(),
			"parser":      parserSchema(),
			"filters":     map[string]any{"type": "array", "items": filterSchema()},
			"batch":       batchSchema(),
			"on_exit":     onExitSchema(),
			"observation": commandObservationSchema(),
		},
	}
}

func scheduleCreateSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          "Set exactly one of once, daily, or interval; daily requires timezone.",
		"additionalProperties": false,
		"required":             []any{"id", "observation"},
		"properties": map[string]any{
			"id":          map[string]any{"type": "string"},
			"name":        map[string]any{"type": "string"},
			"timezone":    map[string]any{"type": "string", "description": "IANA timezone for daily schedules, for example Asia/Shanghai"},
			"once":        onceScheduleSchema(),
			"daily":       dailyScheduleSchema(),
			"interval":    intervalScheduleSchema(),
			"catch_up":    catchUpSchema(),
			"observation": scheduleObservationSchema(),
		},
		"oneOf": []any{
			exclusiveScheduleBranch([]any{"once"}, "daily", "interval"),
			exclusiveScheduleBranch([]any{"daily", "timezone"}, "once", "interval"),
			exclusiveScheduleBranch([]any{"interval"}, "once", "daily"),
		},
	}
}

func exclusiveScheduleBranch(required []any, forbidden ...string) map[string]any {
	forbiddenBranches := make([]any, 0, len(forbidden))
	for _, field := range forbidden {
		forbiddenBranches = append(forbiddenBranches, map[string]any{"required": []any{field}})
	}
	return map[string]any{
		"required": required,
		"not":      map[string]any{"anyOf": forbiddenBranches},
	}
}

func commandObservationSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"kind":     map[string]any{"type": "string"},
			"severity": severitySchema(),
		},
	}
}

func scheduleObservationSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"content"},
		"properties": map[string]any{
			"kind":        map[string]any{"type": "string"},
			"severity":    severitySchema(),
			"content":     map[string]any{"type": "string", "maxLength": MaxScheduleContentChars},
			"attachments": map[string]any{"type": "array", "items": attachmentSchema()},
		},
	}
}

func severitySchema() map[string]any {
	return map[string]any{"type": "string", "enum": []any{"info", "warning", "error", "critical"}}
}

func parserSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"type":           map[string]any{"type": "string", "enum": []any{"text", "jsonl"}},
			"content_field":  map[string]any{"type": "string"},
			"kind_field":     map[string]any{"type": "string"},
			"severity_field": map[string]any{"type": "string"},
			"time_field":     map[string]any{"type": "string"},
			"attachments_field": map[string]any{
				"type":        "string",
				"description": "JSONL field containing an array of attachment objects with path and optional media_type.",
			},
		},
	}
}

func attachmentSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"path"},
		"properties": map[string]any{
			"path":       map[string]any{"type": "string"},
			"media_type": map[string]any{"type": "string"},
		},
	}
}

func filterSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"description":          "Keep output lines matching exactly one predicate: contains or regex.",
		"additionalProperties": false,
		"properties": map[string]any{
			"contains": map[string]any{"type": "string"},
			"regex":    map[string]any{"type": "string"},
			"kind":     map[string]any{"type": "string"},
			"severity": map[string]any{"type": "string", "enum": []any{"info", "warning", "error", "critical"}},
		},
		"oneOf": []any{
			map[string]any{"required": []any{"contains"}},
			map[string]any{"required": []any{"regex"}},
		},
	}
}

func batchSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"description":          "Optional for command sources; omitted values default to a safe 5 second interval and 1000 character batch.",
		"properties": map[string]any{
			"interval_seconds": map[string]any{"type": "integer", "minimum": MinBatchIntervalSeconds, "maximum": MaxBatchIntervalSeconds},
			"max_chars":        map[string]any{"type": "integer", "minimum": 1, "maximum": MaxBatchChars},
		},
	}
}

func onExitSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"notify": map[string]any{"type": "string", "enum": []any{"never", "always", "nonzero"}},
		},
	}
}

func onceScheduleSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"at"},
		"properties": map[string]any{
			"at": map[string]any{"type": "string", "description": "RFC3339 timestamp with timezone"},
		},
	}
}

func dailyScheduleSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"times"},
		"properties": map[string]any{
			"times":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "pattern": "^\\d\\d:\\d\\d$"}},
			"weekdays": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}}},
		},
	}
}

func intervalScheduleSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"every_seconds"},
		"properties": map[string]any{
			"every_seconds": map[string]any{"type": "integer", "minimum": MinIntervalScheduleSecond},
		},
	}
}

func catchUpSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"mode":                 map[string]any{"type": "string", "enum": []any{ScheduleCatchUpNone, ScheduleCatchUpLatest}},
			"max_lateness_minutes": map[string]any{"type": "integer", "minimum": 1, "maximum": 1440},
		},
	}
}

func streamSchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []any{"stdout", "stderr"}}}
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
