package eventcatalog

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/juex-ai/juex/internal/events"
)

type Definition struct {
	Type           string
	Version        int
	ReplayPolicy   events.ReplayPolicy
	Transient      bool
	BrowserVisible bool
	NewPayload     func() any
	Validate       func(any) error
}

type Catalog struct {
	definitions  map[string]Definition
	browserTypes []string
}

func New(definitions ...Definition) (*Catalog, error) {
	catalog := &Catalog{definitions: make(map[string]Definition, len(definitions))}
	for _, definition := range definitions {
		if definition.Type == "" {
			return nil, fmt.Errorf("event catalog: empty event type")
		}
		if definition.Version <= 0 {
			return nil, fmt.Errorf("event catalog: %s schema version must be positive", definition.Type)
		}
		if definition.Transient && definition.ReplayPolicy != "" {
			return nil, fmt.Errorf("event catalog: %s transient schema cannot declare replay policy", definition.Type)
		}
		if !definition.Transient {
			if err := validateReplayPolicy(definition.ReplayPolicy); err != nil {
				return nil, fmt.Errorf("event catalog: %s: %w", definition.Type, err)
			}
		}
		if definition.NewPayload == nil {
			return nil, fmt.Errorf("event catalog: %s payload constructor is nil", definition.Type)
		}
		if _, exists := catalog.definitions[definition.Type]; exists {
			return nil, fmt.Errorf("event catalog: duplicate event type %s", definition.Type)
		}
		catalog.definitions[definition.Type] = definition
		if definition.BrowserVisible {
			catalog.browserTypes = append(catalog.browserTypes, definition.Type)
		}
	}
	return catalog, nil
}

func (c *Catalog) Lookup(eventType string) (Definition, bool) {
	if c == nil {
		return Definition{}, false
	}
	definition, ok := c.definitions[eventType]
	return definition, ok
}

func (c *Catalog) BrowserTypes() []string {
	if c == nil || len(c.browserTypes) == 0 {
		return nil
	}
	return append([]string(nil), c.browserTypes...)
}

func (c *Catalog) Prepare(event events.Event) (events.Event, error) {
	definition, known := c.Lookup(event.Type)
	if !known {
		if event.Transient {
			return event, nil
		}
		if event.SchemaVersion <= 0 {
			return events.Event{}, fmt.Errorf(
				"event catalog: uncataloged durable event %s must declare a positive schema version",
				event.Type,
			)
		}
		if err := validateReplayPolicy(event.ReplayPolicy); err != nil {
			return events.Event{}, fmt.Errorf("event catalog: prepare %s: %w", event.Type, err)
		}
		return event, nil
	}
	if event.Transient != definition.Transient {
		return events.Event{}, fmt.Errorf(
			"event catalog: %s transient=%v, want %v",
			event.Type,
			event.Transient,
			definition.Transient,
		)
	}
	if event.SchemaVersion == 0 {
		event.SchemaVersion = definition.Version
	}
	if event.SchemaVersion != definition.Version {
		return events.Event{}, fmt.Errorf(
			"event catalog: %s schema version %d, want %d",
			event.Type,
			event.SchemaVersion,
			definition.Version,
		)
	}
	if definition.Transient {
		if event.ReplayPolicy != "" {
			return events.Event{}, fmt.Errorf("event catalog: transient %s cannot declare replay policy", event.Type)
		}
		payload, err := normalizePayload(definition, event.Payload)
		if err != nil {
			return events.Event{}, fmt.Errorf("event catalog: prepare %s v%d: %w", event.Type, event.SchemaVersion, err)
		}
		event.Payload = payload
		event.Opaque = false
		return event, nil
	}
	if event.ReplayPolicy == "" {
		event.ReplayPolicy = definition.ReplayPolicy
	}
	if event.ReplayPolicy != definition.ReplayPolicy {
		return events.Event{}, fmt.Errorf(
			"event catalog: %s replay policy %q, want %q",
			event.Type,
			event.ReplayPolicy,
			definition.ReplayPolicy,
		)
	}
	payload, err := normalizePayload(definition, event.Payload)
	if err != nil {
		return events.Event{}, fmt.Errorf("event catalog: prepare %s v%d: %w", event.Type, event.SchemaVersion, err)
	}
	event.Payload = payload
	event.Opaque = false
	return event, nil
}

func (c *Catalog) Decode(event events.Event) (events.Event, error) {
	if event.Transient {
		return events.Event{}, fmt.Errorf("event catalog: transient event %s cannot be replayed", event.Type)
	}
	if event.SchemaVersion <= 0 {
		return events.Event{}, fmt.Errorf("event catalog: replay %s is missing schema version", event.Type)
	}
	if err := validateReplayPolicy(event.ReplayPolicy); err != nil {
		return events.Event{}, fmt.Errorf("event catalog: replay %s: %w", event.Type, err)
	}
	definition, known := c.Lookup(event.Type)
	if !known {
		return decodeUnknown(event)
	}
	if event.SchemaVersion != definition.Version {
		if definition.ReplayPolicy == events.ReplayIgnorable && event.ReplayPolicy == events.ReplayIgnorable {
			event.Opaque = true
			return event, nil
		}
		return events.Event{}, fmt.Errorf(
			"event catalog: replay %s requires unsupported schema version %d (current %d)",
			event.Type,
			event.SchemaVersion,
			definition.Version,
		)
	}
	if event.ReplayPolicy != definition.ReplayPolicy {
		return events.Event{}, fmt.Errorf(
			"event catalog: replay %s v%d policy %q, want %q",
			event.Type,
			event.SchemaVersion,
			event.ReplayPolicy,
			definition.ReplayPolicy,
		)
	}
	payload, err := normalizePayload(definition, event.Payload)
	if err != nil {
		return events.Event{}, fmt.Errorf("event catalog: replay %s v%d: %w", event.Type, event.SchemaVersion, err)
	}
	event.Payload = payload
	event.Opaque = false
	return event, nil
}

func (c *Catalog) BrowserPayload(event events.Event) (events.Event, json.RawMessage, bool, error) {
	if event.Opaque {
		return event, nil, false, nil
	}
	definition, known := c.Lookup(event.Type)
	if !known || !definition.BrowserVisible {
		return event, nil, false, nil
	}
	prepared, err := c.Prepare(event)
	if err != nil {
		return events.Event{}, nil, true, err
	}
	payload, err := json.Marshal(prepared.Payload)
	if err != nil {
		return events.Event{}, nil, true, fmt.Errorf("event catalog: marshal %s browser payload: %w", event.Type, err)
	}
	return prepared, payload, true, nil
}

func decodeUnknown(event events.Event) (events.Event, error) {
	if event.ReplayPolicy == events.ReplayRequired {
		return events.Event{}, fmt.Errorf(
			"event catalog: replay requires unknown event %s v%d",
			event.Type,
			event.SchemaVersion,
		)
	}
	event.Opaque = true
	return event, nil
}

func normalizePayload(definition Definition, payload any) (any, error) {
	if payload == nil {
		return nil, fmt.Errorf("payload is required")
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	target := definition.NewPayload()
	if target == nil || reflect.TypeOf(target).Kind() != reflect.Pointer {
		return nil, fmt.Errorf("payload constructor must return a non-nil pointer")
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return nil, fmt.Errorf("decode payload: %w", err)
	}
	normalized := reflect.ValueOf(target).Elem().Interface()
	if definition.Validate != nil {
		if err := definition.Validate(normalized); err != nil {
			return nil, fmt.Errorf("validate payload: %w", err)
		}
	}
	return normalized, nil
}

func validateReplayPolicy(policy events.ReplayPolicy) error {
	switch policy {
	case events.ReplayRequired, events.ReplayIgnorable:
		return nil
	default:
		return fmt.Errorf("invalid replay policy %q", policy)
	}
}
