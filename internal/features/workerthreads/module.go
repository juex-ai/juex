package workerthreads

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	"github.com/juex-ai/juex/internal/framework/agent"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

func New(manager Manager) *Module { return &Module{manager: manager} }

type Module struct {
	manager Manager
}

func (*Module) ID() runtimemodule.ID { return ModuleID }

func (*Module) StartRuntime(context.Context, runtimemodule.RuntimeContext) error {
	return nil
}

func (m *Module) QuiesceRuntime(context.Context) error {
	if m == nil || m.manager == nil {
		return nil
	}
	return m.manager.StartClose()
}

func (m *Module) CloseRuntime(context.Context) error {
	if m == nil || m.manager == nil {
		return nil
	}
	return m.manager.WaitClose()
}

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]toolcore.Tool, error) {
	return tools(m.manager), nil
}

func (m *Module) PendingInputsAdmitted(_ context.Context, admission runtimemodule.PendingInputAdmission) {
	if m != nil && m.manager != nil {
		m.manager.FinishResultHandoffs(admission.RecordIDs)
	}
}

func (m *Module) ContextRenewed(context.Context) {
	if m != nil && m.manager != nil {
		m.manager.ClearSubscriptions()
	}
}

const (
	ToolCreate    = "thread_create"
	ToolList      = "thread_list"
	ToolStatus    = "thread_status"
	ToolSend      = "thread_send"
	ToolSubscribe = "thread_subscribe"
	ToolStop      = "thread_stop"
	ToolArchive   = "thread_archive"
)

func tools(manager Manager) []toolcore.Tool {
	definitions := toolDefinitions()
	unavailable := func(context.Context, map[string]any) (string, error) {
		return "", errors.New("worker thread manager is unavailable")
	}
	if manager == nil {
		tools := make([]toolcore.Tool, 0, len(definitions))
		for _, definition := range definitions {
			tools = append(tools, definition.Bind(unavailable))
		}
		return tools
	}
	handlers := []toolcore.Handler{
		func(ctx context.Context, input map[string]any) (string, error) {
			subscribe := false
			if raw, ok := input["subscribe"]; ok {
				value, valid := raw.(bool)
				if !valid {
					return "", errors.New("subscribe must be a boolean")
				}
				subscribe = value
			}
			status, err := manager.Create(ctx, toolString(input, "query"), toolString(input, "alias"), toolString(input, "model"), subscribe)
			return marshalWorkerToolResult(status, err)
		},
		func(_ context.Context, _ map[string]any) (string, error) {
			items, err := manager.List()
			return marshalWorkerToolResult(map[string]any{"threads": items}, err)
		},
		func(_ context.Context, input map[string]any) (string, error) {
			status, err := manager.Status(toolString(input, "thread_id"))
			return marshalWorkerToolResult(status, err)
		},
		func(_ context.Context, input map[string]any) (string, error) {
			status, queued, err := manager.Send(toolString(input, "thread_id"), toolString(input, "message"))
			if err != nil {
				return "", err
			}
			return marshalWorkerToolResult(map[string]any{
				"thread_id":     status.ThreadID,
				"state":         status.State,
				"queued":        queued,
				"pending_count": status.PendingCount,
			}, nil)
		},
		func(_ context.Context, input map[string]any) (string, error) {
			subscribed, ok := input["subscribed"].(bool)
			if !ok {
				return "", errors.New("subscribed must be a boolean")
			}
			status, err := manager.Subscribe(toolString(input, "thread_id"), subscribed)
			return marshalWorkerToolResult(status, err)
		},
		func(ctx context.Context, input map[string]any) (string, error) {
			id := toolString(input, "thread_id")
			if err := manager.Stop(ctx, id); err != nil {
				return "", err
			}
			return marshalWorkerToolResult(map[string]any{"thread_id": id, "stopped": true}, nil)
		},
		func(ctx context.Context, input map[string]any) (string, error) {
			id := toolString(input, "thread_id")
			if err := manager.Archive(ctx, id); err != nil {
				return "", err
			}
			return marshalWorkerToolResult(map[string]any{"thread_id": id, "archived": true}, nil)
		},
	}
	provided := make([]toolcore.Tool, 0, len(definitions))
	for i, definition := range definitions {
		provided = append(provided, definition.Bind(handlers[i]))
	}
	return provided
}

func toolDefinitions() []toolcore.ToolDefinition {
	id := map[string]any{"type": "string"}
	return []toolcore.ToolDefinition{
		{
			Name:            ToolCreate,
			Group:           toolcore.ToolGroupWorkerThread,
			ExecutionPolicy: toolcore.ToolExecutionSerial,
			Description:     "Create a managed Worker Thread and start its query asynchronously. Set subscribe to receive terminal results.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query":     map[string]any{"type": "string"},
					"alias":     map[string]any{"type": "string"},
					"model":     map[string]any{"type": "string"},
					"subscribe": map[string]any{"type": "boolean"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:            ToolList,
			Group:           toolcore.ToolGroupWorkerThread,
			ExecutionPolicy: toolcore.ToolExecutionSerial,
			Description:     "List direct Worker Threads currently managed by this Thread.",
			Schema:          map[string]any{"type": "object", "properties": map[string]any{}},
		},
		{
			Name:            ToolStatus,
			Group:           toolcore.ToolGroupWorkerThread,
			ExecutionPolicy: toolcore.ToolExecutionSerial,
			Description:     "Read the runtime status and latest result of an active managed Worker Thread.",
			Schema: map[string]any{
				"type": "object", "properties": map[string]any{"thread_id": id}, "required": []string{"thread_id"},
			},
		},
		{
			Name:            ToolSend,
			Group:           toolcore.ToolGroupWorkerThread,
			ExecutionPolicy: toolcore.ToolExecutionSerial,
			Description:     "Send a message to a managed Worker Thread; busy Threads queue it as durable pending input.",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"thread_id": id, "message": map[string]any{"type": "string"}},
				"required":   []string{"thread_id", "message"},
			},
		},
		{
			Name:            ToolSubscribe,
			Group:           toolcore.ToolGroupWorkerThread,
			ExecutionPolicy: toolcore.ToolExecutionSerial,
			Description:     "Enable or disable terminal result notifications for a managed Worker Thread.",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"thread_id": id, "subscribed": map[string]any{"type": "boolean"}},
				"required":   []string{"thread_id", "subscribed"},
			},
		},
		{
			Name:            ToolStop,
			Group:           toolcore.ToolGroupWorkerThread,
			ExecutionPolicy: toolcore.ToolExecutionSerial,
			Description:     "Stop and close an active managed Worker Thread while preserving its durable history.",
			Schema: map[string]any{
				"type": "object", "properties": map[string]any{"thread_id": id}, "required": []string{"thread_id"},
			},
		},
		{
			Name:            ToolArchive,
			Group:           toolcore.ToolGroupWorkerThread,
			ExecutionPolicy: toolcore.ToolExecutionSerial,
			Description:     "Archive an idle, unsubscribed Worker Thread after all pending input and result deliveries have settled.",
			Schema: map[string]any{
				"type": "object", "properties": map[string]any{"thread_id": id}, "required": []string{"thread_id"},
			},
		},
	}
}

func toolString(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return strings.TrimSpace(value)
}

func marshalWorkerToolResult(value any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Manager is the lifecycle and execution surface consumed by this feature.
type Manager interface {
	Create(context.Context, string, string, string, bool) (agent.WorkerThreadStatus, error)
	List() ([]agent.WorkerThreadStatus, error)
	Status(string) (agent.WorkerThreadStatus, error)
	Send(string, string) (agent.WorkerThreadStatus, bool, error)
	Subscribe(string, bool) (agent.WorkerThreadStatus, error)
	Stop(context.Context, string) error
	Archive(context.Context, string) error
	StartClose() error
	WaitClose() error
	FinishResultHandoffs([]string)
	ClearSubscriptions()
}
