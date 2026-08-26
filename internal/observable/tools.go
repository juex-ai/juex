package observable

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"

	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/tools"
)

const (
	defaultObservationToolLimit = 20
	maxObservationToolLimit     = 100
	observableGuidePointer      = `Guide available via skill_load("juex-observables").`
)

const ModuleID runtimemodule.ID = "observables"

type Module struct {
	mu      sync.RWMutex
	manager *Manager
	options ManagerOptions
	owned   bool
}

func NewModule(manager *Manager) *Module { return &Module{manager: manager} }

// NewRuntimeModule defers config loading and durable store construction until
// the runtime Module lifecycle starts. Producer startup remains an explicit
// App action after Session recovery barriers have been published.
func NewRuntimeModule(options ManagerOptions) *Module {
	return &Module{options: options, owned: true}
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]tools.Tool, error) {
	if m == nil {
		return unavailableObservableTools(), nil
	}
	m.mu.RLock()
	manager := m.manager
	m.mu.RUnlock()
	if manager == nil {
		return unavailableObservableTools(), nil
	}
	return observableTools(manager), nil
}

func (m *Module) StartRuntime(context.Context, runtimemodule.RuntimeContext) error {
	if m == nil || !m.owned {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.manager != nil {
		return nil
	}
	manager, err := NewManager(m.options)
	if err != nil {
		return err
	}
	m.manager = manager
	return nil
}

// StartAll starts producers for an App-owned manager after the App has
// published its Session recovery boundary. Injected managers retain lifecycle
// ownership in their caller and are intentionally left untouched.
func (m *Module) StartAll(ctx context.Context) error {
	if m == nil || !m.owned {
		return nil
	}
	m.mu.RLock()
	manager := m.manager
	m.mu.RUnlock()
	if manager == nil {
		return fmt.Errorf("observable: runtime module manager is not started")
	}
	return manager.StartAll(ctx)
}

func (m *Module) QuiesceRuntime(context.Context) error { return m.closeOwned() }

func (m *Module) CloseRuntime(context.Context) error { return m.closeOwned() }

func (m *Module) Manager() *Manager {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.manager
}

func (m *Module) closeOwned() error {
	if m == nil || !m.owned {
		return nil
	}
	m.mu.RLock()
	manager := m.manager
	m.mu.RUnlock()
	return manager.Close()
}

func unavailableObservableTools() []tools.Tool {
	definitions := ToolDefinitions()
	provided := make([]tools.Tool, 0, len(definitions))
	for _, definition := range definitions {
		provided = append(provided, definition.Bind(func(context.Context, map[string]any) (string, error) {
			return "", fmt.Errorf("observable manager is unavailable")
		}))
	}
	return provided
}

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
			Description: "List configured Observables and runtime status; call before creating one. " + observableGuidePointer,
			Schema:      map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
		{
			Name:        "observable_create",
			Group:       tools.ToolGroupObservable,
			Description: "Create and start a command Observable; use schedule_create for timed work. " + observableGuidePointer,
			Schema:      commandCreateSchema(),
		},
		{
			Name:        "schedule_create",
			Group:       tools.ToolGroupObservable,
			Description: "List first; reuse equivalents; never probe or command-poll. " + observableGuidePointer,
			Schema:      scheduleCreateSchema(),
		},
		{
			Name:        "observable_start",
			Group:       tools.ToolGroupObservable,
			Description: "Temporarily start an Observable for this process. " + observableGuidePointer,
			Schema:      idSchema,
		},
		{
			Name:        "observable_stop",
			Group:       tools.ToolGroupObservable,
			Description: "Temporarily stop an Observable; delete for permanent removal. " + observableGuidePointer,
			Schema:      idSchema,
		},
		{
			Name:        "observable_delete",
			Group:       tools.ToolGroupObservable,
			Description: "Permanently delete and stop a project-owned Observable; extension definitions are read-only. Use stop for temporary pause. " + observableGuidePointer,
			Schema:      idSchema,
		},
		{
			Name:        "observable_observations",
			Group:       tools.ToolGroupObservable,
			Description: "List recent durable Observations, optionally for one Observable. " + observableGuidePointer,
			Schema: map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"id":    map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer"},
				},
			},
		},
	}
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
	Monthly     *MonthlySchedule        `json:"monthly,omitempty"`
	Interval    *IntervalSchedule       `json:"interval,omitempty"`
	CatchUp     CatchUpSpec             `json:"catch_up,omitempty"`
	Observation ScheduleObservationSpec `json:"observation"`
}

func commandSpecFromCreateInput(in map[string]any) (Spec, error) {
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
		Monthly:     input.Monthly,
		Interval:    input.Interval,
		CatchUp:     input.CatchUp,
		Observation: input.Observation,
	})
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
		"required":             []any{"command"},
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
		"additionalProperties": false,
		"required":             []any{"observation"},
		"properties": map[string]any{
			"id":          map[string]any{"type": "string"},
			"name":        map[string]any{"type": "string"},
			"timezone":    map[string]any{"type": "string"},
			"once":        onceScheduleSchema(),
			"daily":       dailyScheduleSchema(),
			"monthly":     monthlyScheduleSchema(),
			"interval":    intervalScheduleSchema(),
			"catch_up":    catchUpSchema(),
			"observation": scheduleObservationSchema(),
		},
		"oneOf": []any{
			map[string]any{"required": []any{"once"}},
			map[string]any{"required": []any{"daily"}},
			map[string]any{"required": []any{"monthly"}},
			map[string]any{"required": []any{"interval"}},
		},
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
			"content":     map[string]any{"type": "string"},
			"attachments": map[string]any{"type": "array", "items": attachmentSchema()},
		},
	}
}

func severitySchema() map[string]any {
	return map[string]any{"type": "string"}
}

func parserSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"type":              map[string]any{"type": "string"},
			"content_field":     map[string]any{"type": "string"},
			"kind_field":        map[string]any{"type": "string"},
			"severity_field":    map[string]any{"type": "string"},
			"time_field":        map[string]any{"type": "string"},
			"attachments_field": map[string]any{"type": "string"},
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
		"additionalProperties": false,
		"properties": map[string]any{
			"contains": map[string]any{"type": "string"},
			"regex":    map[string]any{"type": "string"},
			"kind":     map[string]any{"type": "string"},
			"severity": map[string]any{"type": "string"},
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
		"properties": map[string]any{
			"interval_seconds": map[string]any{"type": "integer"},
			"max_chars":        map[string]any{"type": "integer"},
		},
	}
}

func onExitSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"notify": map[string]any{"type": "string"},
		},
	}
}

func onceScheduleSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"at"},
		"properties": map[string]any{
			"at": map[string]any{"type": "string"},
		},
	}
}

func dailyScheduleSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"times"},
		"properties": map[string]any{
			"times":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"weekdays": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
}

func monthlyScheduleSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"days", "times"},
		"properties": map[string]any{
			"days":  map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			"times": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	}
}

func intervalScheduleSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"every_seconds"},
		"properties": map[string]any{
			"every_seconds": map[string]any{"type": "integer"},
		},
	}
}

func catchUpSchema() map[string]any {
	return map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"mode":                 map[string]any{"type": "string"},
			"max_lateness_minutes": map[string]any{"type": "integer"},
		},
	}
}

func streamSchema() map[string]any {
	return map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
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
