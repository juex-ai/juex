package eventcatalog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/provenance"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	runtimemodule "github.com/juex-ai/juex/internal/runtime/module"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestDefaultCatalogPreparesAndDecodesStableEvent(t *testing.T) {
	catalog := Default()
	prepared, err := catalog.Prepare(events.Event{
		Type:    "turn.started",
		TurnID:  "turn-1",
		Payload: juexruntime.TurnStartedPayload{Input: "hello", Kind: "user"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SchemaVersion != 1 || prepared.ReplayPolicy != events.ReplayRequired {
		t.Fatalf("prepared schema = v%d/%q", prepared.SchemaVersion, prepared.ReplayPolicy)
	}

	wire, err := json.Marshal(prepared)
	if err != nil {
		t.Fatal(err)
	}
	var replayed events.Event
	if err := json.Unmarshal(wire, &replayed); err != nil {
		t.Fatal(err)
	}
	decoded, err := catalog.Decode(replayed)
	if err != nil {
		t.Fatal(err)
	}
	payload, ok := decoded.Payload.(juexruntime.TurnStartedPayload)
	if !ok || payload.Input != "hello" || payload.Kind != "user" {
		t.Fatalf("decoded payload = %#v", decoded.Payload)
	}
	if decoded.Opaque {
		t.Fatal("known current schema decoded as opaque")
	}
}

func TestDefaultCatalogRejectsMalformedStablePayload(t *testing.T) {
	_, err := Default().Prepare(events.Event{
		Type:    "turn.started",
		Payload: map[string]any{"input": 42},
	})
	if err == nil || !strings.Contains(err.Error(), "turn.started") {
		t.Fatalf("Prepare() error = %v, want typed payload error", err)
	}
}

func TestDefaultCatalogValidatesToolExecutionIdentityAndOutcome(t *testing.T) {
	call := toolevents.ToolCallPayload{
		Name: "write", ToolUseID: "call-1", Iter: 2, CallIndex: 0,
		MessageID: "assistant-1",
	}
	responded, err := Default().Prepare(events.Event{Type: "llm.responded", Payload: juexruntime.LLMRespondedPayload{
		Iter: 2, MessageID: "assistant-1", EpochID: "epoch-1", RequestDigest: strings.Repeat("a", 64), ToolCalls: []toolevents.ToolCallPayload{call},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if responded.SchemaVersion != 3 {
		t.Fatalf("llm.responded schema = %d, want 3", responded.SchemaVersion)
	}

	call.MessageID = "different-assistant"
	if _, err := Default().Prepare(events.Event{Type: "llm.responded", Payload: juexruntime.LLMRespondedPayload{
		Iter: 2, MessageID: "assistant-1", EpochID: "epoch-1", RequestDigest: strings.Repeat("a", 64), ToolCalls: []toolevents.ToolCallPayload{call},
	}}); err == nil {
		t.Fatal("llm.responded mismatched tool identity was accepted")
	}

	call.MessageID = "assistant-1"
	completed := toolevents.Completed(call, 60, 2, "ok", nil)
	if _, err := Default().Prepare(events.Event{Type: toolevents.CompletedType, Payload: completed}); err == nil {
		t.Fatal("tool.completed without durable outcome was accepted")
	}
	completed.Outcome = &toolevents.RecordedOutcome{
		MessageID: "result-1",
		Block: llm.Block{
			Type: llm.BlockToolResult, ToolUseID: call.ToolUseID,
			ToolName: call.Name, Content: "ok",
		},
	}
	prepared, err := Default().Prepare(events.Event{Type: toolevents.CompletedType, Payload: completed})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SchemaVersion != 2 {
		t.Fatalf("tool.completed schema = %d, want 2", prepared.SchemaVersion)
	}
}

func TestDefaultCatalogValidatesProviderRequestProvenance(t *testing.T) {
	message := llm.TextMessage(llm.RoleUser, "hello")
	message.ID = "user-1"
	epoch, err := provenance.BuildRequestEpoch(provenance.RequestInput{
		Provider: provenance.SafeProvider{ID: "test", Model: "model"},
		History:  []llm.Message{message},
	})
	if err != nil {
		t.Fatal(err)
	}
	epoch.EpochID = "epoch-1"
	prepared, err := Default().Prepare(events.Event{Type: provenance.RequestEpochType, Payload: provenance.RequestEpochPayload{Epoch: epoch}})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SchemaVersion != 1 || prepared.ReplayPolicy != events.ReplayRequired {
		t.Fatalf("request epoch schema = v%d/%q", prepared.SchemaVersion, prepared.ReplayPolicy)
	}
	requested, err := Default().Prepare(events.Event{Type: "llm.requested", Payload: juexruntime.LLMRequestedPayload{
		Iter: 0, Purpose: "turn", EpochID: epoch.EpochID, RequestDigest: epoch.RequestDigest,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if requested.SchemaVersion != 3 {
		t.Fatalf("llm.requested schema = %d, want 3", requested.SchemaVersion)
	}
	if _, err := Default().Prepare(events.Event{Type: "llm.requested", Payload: juexruntime.LLMRequestedPayload{
		Iter: 0, Purpose: "unknown", EpochID: epoch.EpochID, RequestDigest: epoch.RequestDigest,
	}}); err == nil {
		t.Fatal("llm.requested with unknown purpose was accepted")
	}
	errored, err := Default().Prepare(events.Event{Type: "llm.errored", Payload: juexruntime.LLMErroredPayload{
		Iter: 0, Purpose: "turn", Model: "test:model", Error: "status 503",
		EpochID: epoch.EpochID, RequestDigest: epoch.RequestDigest,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if errored.SchemaVersion != 1 || errored.ReplayPolicy != events.ReplayRequired {
		t.Fatalf("llm.errored schema = v%d/%q", errored.SchemaVersion, errored.ReplayPolicy)
	}
	if _, err := Default().Prepare(events.Event{Type: "llm.errored", Payload: juexruntime.LLMErroredPayload{
		Iter: 0, Purpose: "turn", Error: "status 503",
	}}); err == nil {
		t.Fatal("llm.errored without epoch identity was accepted")
	}
	compactionRetry, err := Default().Prepare(events.Event{Type: "llm.retry", Payload: juexruntime.LLMRetryPayload{
		Purpose: "compaction", EpochID: epoch.EpochID, RequestDigest: epoch.RequestDigest,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if compactionRetry.SchemaVersion != 3 {
		t.Fatalf("llm.retry schema = %d, want 3", compactionRetry.SchemaVersion)
	}
	if _, err := Default().Prepare(events.Event{Type: provenance.RequestEpochType, Payload: provenance.RequestEpochPayload{}}); err == nil {
		t.Fatal("empty request epoch was accepted")
	}
}

func TestDefaultCatalogOwnsPolicyLifecycleFacts(t *testing.T) {
	payload := juexruntime.PolicyCompletedPayload{
		ModuleID:    "quota",
		PolicyPoint: runtimemodule.PolicyPointToolBefore,
		Name:        "budget-check",
		Source:      "project",
		ToolName:    "exec_command",
		DurationMS:  4,
	}
	prepared, err := Default().Prepare(events.Event{Type: "policy.completed", Payload: payload})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SchemaVersion != 1 || prepared.ReplayPolicy != events.ReplayIgnorable {
		t.Fatalf("policy completed schema = v%d/%q", prepared.SchemaVersion, prepared.ReplayPolicy)
	}
	raw, err := json.Marshal(prepared.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), `{"module_id":"quota","policy_point":"tool_before","name":"budget-check","source":"project","tool_name":"exec_command","duration_ms":4,"exit_code":0}`; got != want {
		t.Fatalf("policy completed payload = %s, want %s", got, want)
	}
	if _, err := Default().Prepare(events.Event{Type: "policy.completed", Payload: juexruntime.PolicyCompletedPayload{}}); err == nil {
		t.Fatal("policy.completed without Framework ownership was accepted")
	}
	payload.PolicyPoint = runtimemodule.PolicyPoint("forged-point")
	if _, err := Default().Prepare(events.Event{Type: "policy.completed", Payload: payload}); err == nil {
		t.Fatal("policy.completed with noncanonical policy point was accepted")
	}
}

func TestDefaultCatalogRequiresUncatalogedDurableEventsToDeclareReplayContract(t *testing.T) {
	_, err := Default().Prepare(events.Event{
		Type:    "plugin.notice",
		Payload: map[string]any{"value": 1},
	})
	if err == nil || !strings.Contains(err.Error(), "must declare") {
		t.Fatalf("Prepare() error = %v, want missing declaration error", err)
	}
}

func TestDefaultCatalogKeepsTransientSchemaOutOfReplayContract(t *testing.T) {
	prepared, err := Default().Prepare(events.Event{
		Type:      "llm.output_delta",
		Transient: true,
		Payload:   juexruntime.LLMOutputDeltaPayload{Kind: "text", Text: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.SchemaVersion != 1 || prepared.ReplayPolicy != "" {
		t.Fatalf("transient schema = v%d/%q, want v1 without replay policy", prepared.SchemaVersion, prepared.ReplayPolicy)
	}
}

func TestDefaultCatalogReplayPolicyForUnknownSchemas(t *testing.T) {
	tests := []struct {
		name       string
		event      events.Event
		wantError  bool
		wantOpaque bool
	}{
		{
			name: "unknown required type fails closed",
			event: events.Event{
				Type: "plugin.required", SchemaVersion: 3,
				ReplayPolicy: events.ReplayRequired, Payload: json.RawMessage(`{"value":1}`),
			},
			wantError: true,
		},
		{
			name: "unknown ignorable type stays opaque",
			event: events.Event{
				Type: "plugin.notice", SchemaVersion: 2,
				ReplayPolicy: events.ReplayIgnorable, Payload: json.RawMessage(`{"value":1}`),
			},
			wantOpaque: true,
		},
		{
			name: "unsupported ignorable version stays opaque",
			event: events.Event{
				Type: "policy.trace", SchemaVersion: 2,
				ReplayPolicy: events.ReplayIgnorable, Payload: json.RawMessage(`{"text":"old"}`),
			},
			wantOpaque: true,
		},
		{
			name: "unsupported required version fails closed",
			event: events.Event{
				Type: "policy.trace", SchemaVersion: 2,
				ReplayPolicy: events.ReplayRequired, Payload: json.RawMessage(`{"text":"old"}`),
			},
			wantError: true,
		},
		{
			name: "required family cannot be downgraded by record",
			event: events.Event{
				Type: "turn.started", SchemaVersion: 2,
				ReplayPolicy: events.ReplayIgnorable, Payload: json.RawMessage(`{"input":"old"}`),
			},
			wantError: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decoded, err := Default().Decode(test.event)
			if test.wantError {
				if err == nil {
					t.Fatal("Decode() error = nil")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Opaque != test.wantOpaque {
				t.Fatalf("Opaque = %v, want %v", decoded.Opaque, test.wantOpaque)
			}
		})
	}
}

func TestDefaultCatalogOwnsBrowserProjection(t *testing.T) {
	prepared, payload, visible, err := Default().BrowserPayload(events.Event{
		Type:    "turn.started",
		Payload: juexruntime.TurnStartedPayload{Input: "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !visible || prepared.SchemaVersion != 1 {
		t.Fatalf("visible = %v, schema version = %d", visible, prepared.SchemaVersion)
	}
	if string(payload) != `{"input":"hello"}` {
		t.Fatalf("payload = %s", payload)
	}
	types := Default().BrowserTypes()
	if !contains(types, "turn.started") || contains(types, "finish.attempted") {
		t.Fatalf("browser types = %v", types)
	}
}

func TestCatalogRegistrationAndLookupAreImmutable(t *testing.T) {
	catalog, err := New(Definition{
		Type:         "test.fact",
		Version:      4,
		ReplayPolicy: events.ReplayRequired,
		NewPayload: func() any {
			return &struct {
				Value string `json:"value"`
			}{}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	definition, ok := catalog.Lookup("test.fact")
	if !ok || definition.Version != 4 {
		t.Fatalf("definition = %+v, ok = %v", definition, ok)
	}
	if _, err := New(definition, definition); err == nil {
		t.Fatal("duplicate definition error = nil")
	}
	if !reflect.DeepEqual(catalog.BrowserTypes(), []string(nil)) {
		t.Fatalf("browser types = %v", catalog.BrowserTypes())
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
