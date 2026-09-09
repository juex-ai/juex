package config

// Parser fixtures intentionally contain only the identities exercised here.
// The product inventory and minimal membership are tested by App's catalog.
func testModuleInventory() ModuleInventory {
	return NewModuleInventory([]ModuleDefinition{
		{ID: "base", Minimal: true}, {ID: "shell", Minimal: true},
		{ID: "skills"}, {ID: "hooks"}, {ID: "goal"}, {ID: "notes"},
		{ID: "mcp"}, {ID: "scratchpad"}, {ID: "extensions"}, {ID: "worker-threads"},
	})
}
