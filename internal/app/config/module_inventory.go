package config

import "fmt"

// ModuleDefinition declares preset membership without constructing a capability.
type ModuleDefinition struct {
	ID      string
	Minimal bool
}

// ModuleInventory is an immutable input supplied by the composition root.
// Copies of Config share the inventory; sparse module switches remain separate.
type ModuleInventory struct {
	definitions []ModuleDefinition
	byID        map[string]ModuleDefinition
}

func NewModuleInventory(definitions []ModuleDefinition) ModuleInventory {
	inventory := ModuleInventory{definitions: append([]ModuleDefinition(nil), definitions...), byID: make(map[string]ModuleDefinition, len(definitions))}
	for _, definition := range definitions {
		if definition.ID == "" {
			panic("config: empty module identity")
		}
		if _, exists := inventory.byID[definition.ID]; exists {
			panic("config: duplicate module identity " + definition.ID)
		}
		inventory.byID[definition.ID] = definition
	}
	return inventory
}

func (i ModuleInventory) Definitions() []ModuleDefinition {
	return append([]ModuleDefinition(nil), i.definitions...)
}

func (i ModuleInventory) Lookup(id string) (ModuleDefinition, bool) {
	definition, ok := i.byID[id]
	return definition, ok
}

func (i ModuleInventory) validate() error {
	if i.byID == nil {
		return fmt.Errorf("config: module inventory is required")
	}
	return nil
}
