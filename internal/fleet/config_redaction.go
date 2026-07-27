package fleet

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

const redactedEnvironmentValue = "[REDACTED_ENV]"

// RedactAgentConfig replaces environment.variables values with a stable
// placeholder before a workspace config crosses the Fleet HTTP boundary.
func RedactAgentConfig(state AgentConfig) (AgentConfig, error) {
	if !state.Exists || state.Content == "" {
		return state, nil
	}
	doc, variables, err := parseEnvironmentVariables([]byte(state.Content))
	if err != nil {
		return AgentConfig{}, fmt.Errorf("fleet: redact workspace config: %w", err)
	}
	if variables == nil {
		return state, nil
	}
	for i := 1; i < len(variables.Content); i += 2 {
		value := variables.Content[i]
		value.Kind = yaml.ScalarNode
		value.Tag = "!!str"
		value.Value = redactedEnvironmentValue
		value.Style = yaml.DoubleQuotedStyle
	}
	data, err := marshalYAMLDocument(doc)
	if err != nil {
		return AgentConfig{}, fmt.Errorf("fleet: redact workspace config: %w", err)
	}
	state.Content = string(data)
	return state, nil
}

func mergeRedactedEnvironmentValues(content []byte, current AgentConfig) ([]byte, error) {
	doc, variables, err := parseEnvironmentVariables(content)
	if err != nil || variables == nil {
		return content, err
	}
	needsMerge := false
	for i := 1; i < len(variables.Content); i += 2 {
		if variables.Content[i].Value == redactedEnvironmentValue {
			needsMerge = true
			break
		}
	}
	if !needsMerge {
		return content, nil
	}
	if !current.Exists {
		return nil, fmt.Errorf("fleet: redacted environment placeholder has no existing value")
	}
	_, existingVariables, err := parseEnvironmentVariables([]byte(current.Content))
	if err != nil {
		return nil, err
	}
	existing := map[string]*yaml.Node{}
	if existingVariables != nil {
		for i := 0; i+1 < len(existingVariables.Content); i += 2 {
			existing[existingVariables.Content[i].Value] = existingVariables.Content[i+1]
		}
	}
	for i := 0; i+1 < len(variables.Content); i += 2 {
		key := variables.Content[i].Value
		value := variables.Content[i+1]
		if value.Value != redactedEnvironmentValue {
			continue
		}
		previous, ok := existing[key]
		if !ok {
			return nil, fmt.Errorf("fleet: redacted environment placeholder for %q has no existing value", key)
		}
		*value = *previous
	}
	return marshalYAMLDocument(doc)
}

func parseEnvironmentVariables(data []byte) (*yaml.Node, *yaml.Node, error) {
	var doc yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&doc); err != nil {
		return nil, nil, err
	}
	if len(doc.Content) == 0 {
		return &doc, nil, nil
	}
	root := doc.Content[0]
	environmentNode := mappingValue(root, "environment")
	if environmentNode == nil {
		return &doc, nil, nil
	}
	variables := mappingValue(environmentNode, "variables")
	if variables != nil && variables.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("environment.variables must be a mapping")
	}
	return &doc, variables, nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func marshalYAMLDocument(doc *yaml.Node) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(doc); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
