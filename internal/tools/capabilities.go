package tools

import "fmt"

// ToolAvailability describes the complete tool set for one serving registry.
// It contains no configuration or module implementation details.
type ToolAvailability struct {
	names map[string]struct{}
}

func (a ToolAvailability) Has(name string) bool {
	_, ok := a.names[name]
	return ok
}

func (a ToolAvailability) HasAll(names ...string) bool {
	for _, name := range names {
		if !a.Has(name) {
			return false
		}
	}
	return true
}

// ResolveTools produces static definitions after all contributions are known.
// Unresolved definitions must already be safe to use independently. Adapters
// may refine descriptions and schemas, but cannot change execution or identity.
func ResolveTools(input []Tool) ([]Tool, error) {
	available := ToolAvailability{names: make(map[string]struct{}, len(input))}
	for _, tool := range input {
		if available.Has(tool.Name) {
			return nil, fmt.Errorf("tools: %s already contributed", tool.Name)
		}
		available.names[tool.Name] = struct{}{}
	}
	resolved := make([]Tool, 0, len(input))
	for _, tool := range input {
		tool = tool.Clone()
		if tool.ResolveDefinition != nil {
			definition := tool.ResolveDefinition(available)
			if definition.Name != tool.Name || definition.Group != tool.Group || definition.TimeoutPolicy != tool.TimeoutPolicy || definition.TimeoutSeconds != tool.TimeoutSeconds {
				return nil, fmt.Errorf("tools: %s definition adapter changed identity or execution policy", tool.Name)
			}
			tool.Description = definition.Description
			tool.Schema = cloneSchemaMap(definition.Schema)
			tool.ResolveDefinition = nil
		}
		if guide, ok := tool.Group.GuideSkill(); ok && available.Has("skill_load") {
			tool.Description += fmt.Sprintf(` Guide available via skill_load("%s").`, guide)
		}
		resolved = append(resolved, tool)
	}
	return resolved, nil
}
