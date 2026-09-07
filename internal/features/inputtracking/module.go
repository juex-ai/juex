// Package inputtracking presents the Framework's durable input checklist.
package inputtracking

import (
	"context"
	"encoding/json"
	"fmt"

	toolcore "github.com/juex-ai/juex/internal/foundation/tools"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
)

const ModuleID = "input-tracking"
const ToolCheck = "check_inputs"

type Module struct{ tracker runtimemodule.InputTracker }

func New(tracker runtimemodule.InputTracker) *Module { return &Module{tracker: tracker} }
func (*Module) ID() runtimemodule.ID                 { return ModuleID }

const guidance = `Input checklist: the following entries are delivered user inputs that still need attention. Preserve ongoing work while applying later amendments. Unchecked does not mean no progress was made. History includes handled requests; do not restart them merely because they remain in context.
Use check_inputs only after handling every part of an input. Answer questions before checking them (answer text may precede the tool call in the same response). Finish other tools in a previous response before checking. Keep partial work, failures, waiting requests, and still-applicable constraints unchecked. A new status question does not replace the original task. If the user explicitly cancels or fully replaces a request, handle that instruction before checking the obsolete input. Do not cancel work just to empty the list.
This is a reminder, not a new user message or a reason to keep running while waiting. Entries preserve user priority; later user amendments apply. Entries with external content references require the existing file/media tools.`

func (m *Module) Tools(context.Context, runtimemodule.ToolContext) ([]toolcore.Tool, error) {
	definition := toolcore.ToolDefinition{
		Name: ToolCheck, Group: toolcore.ToolGroupThreadState, ExecutionPolicy: toolcore.ToolExecutionSerial,
		Guide:       toolcore.ToolGuide{Loader: "skill_load", Name: "juex-thread-state"},
		Description: "Check handled input IDs from the checklist after answering or completing the work. Keep partial work, failures, waiting, and active constraints unchecked. Rechecking is idempotent.",
		Schema: map[string]any{
			"type": "object", "additionalProperties": false,
			"properties": map[string]any{"input_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 256}},
			"required":   []string{"input_ids"},
		},
	}
	return []toolcore.Tool{definition.Bind(func(ctx context.Context, input map[string]any) (string, error) {
		if m.tracker == nil {
			return "", fmt.Errorf("input tracking is unavailable")
		}
		data, err := json.Marshal(input["input_ids"])
		if err != nil {
			return "", err
		}
		var ids []string
		if err := json.Unmarshal(data, &ids); err != nil || len(ids) == 0 {
			return "", fmt.Errorf("input_ids must be a nonempty array of strings")
		}
		confirmed, err := m.tracker.CheckInputs(ctx, ids)
		if err != nil {
			return "", err
		}
		result, err := json.Marshal(map[string]any{"checked_input_ids": confirmed})
		return string(result), err
	})}, nil
}

func (m *Module) Context(ctx context.Context, request runtimemodule.ContextRequest) ([]runtimemodule.ContextSection, error) {
	if request.Purpose != runtimemodule.ContextPurposeProviderIteration || m.tracker == nil {
		return nil, nil
	}
	inputs, err := m.tracker.UncheckedInputs(ctx)
	if err != nil {
		return nil, err
	}
	section := func(key, text string) runtimemodule.ContextSection {
		return runtimemodule.ContextSection{Key: key, Source: "runtime", Text: text, Projection: runtimemodule.ContextProjectionRuntimeMessage, MessageID: "runtime-" + key, Budget: runtimemodule.UnboundedContextBudget()}
	}
	sections := []runtimemodule.ContextSection{section("input-checklist", fmt.Sprintf("%s\nDelivered unchecked inputs: %d.", guidance, len(inputs)))}
	for _, input := range inputs {
		// Separate fragments keep each input ID discoverable even when ordinary
		// context projection externalizes an individual large input.
		sections = append(sections, section("input-"+input.ID, fmt.Sprintf("Unchecked input [%s] (original message blocks):\n%s", input.ID, input.Content)))
	}
	return sections, nil
}

func (m *Module) CompactionContribution(ctx context.Context) (runtimemodule.CompactionContribution, error) {
	inputs, err := m.tracker.UncheckedInputs(ctx)
	if err != nil {
		return runtimemodule.CompactionContribution{}, err
	}
	if len(inputs) == 0 {
		return runtimemodule.CompactionContribution{}, nil
	}
	ids := make([]string, 0, len(inputs))
	for _, input := range inputs {
		ids = append(ids, input.ID)
	}
	state, err := json.Marshal(map[string]any{"unchecked_input_ids": ids})
	if err != nil {
		return runtimemodule.CompactionContribution{}, err
	}
	canonical := "The durable checklist is authoritative and will be supplied again on the next request. Do not reopen checked historical requests. Unchecked does not mean no progress was made.\n" + string(state)
	return runtimemodule.CompactionContribution{
		State: string(state), Section: "Input Checklist",
		Guidance:  "Respect recorded check_inputs outcomes when describing remaining work; do not infer unfinished work from an old user message alone. Current input content is retained durably outside the summary.",
		Reconcile: func(ctx context.Context, _ string) (string, error) { return canonical, ctx.Err() },
	}, nil
}
