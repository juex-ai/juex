package eventcatalog

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/events"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
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
				Type: "hook.trace", SchemaVersion: 2,
				ReplayPolicy: events.ReplayIgnorable, Payload: json.RawMessage(`{"text":"old"}`),
			},
			wantOpaque: true,
		},
		{
			name: "unsupported required version fails closed",
			event: events.Event{
				Type: "hook.trace", SchemaVersion: 2,
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
