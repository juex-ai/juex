package fleet

import (
	"bytes"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

const (
	redactedEnvironmentValue       = "[REDACTED_ENV]"
	literalEnvironmentValueYAMLTag = "!juex/literal"
)

// RedactAgentConfig replaces environment.variables values with a stable
// placeholder before a workspace config crosses the Fleet HTTP boundary.
func RedactAgentConfig(state AgentConfig) (AgentConfig, error) {
	if !state.Exists || state.Content == "" {
		return state, nil
	}
	docs, variables, err := parseEnvironmentVariables([]byte(state.Content))
	if err != nil {
		return AgentConfig{}, fmt.Errorf("fleet: redact workspace config: %w", err)
	}
	if variables == nil {
		return state, nil
	}
	err = walkEnvironmentVariableValues(variables, func(_ string, value *yaml.Node) error {
		scalar, scalarErr := scalarEnvironmentValue(value)
		if scalarErr != nil {
			return scalarErr
		}
		scalar.Kind = yaml.ScalarNode
		scalar.Tag = "!!str"
		scalar.Value = redactedEnvironmentValue
		scalar.Style = yaml.DoubleQuotedStyle
		return nil
	})
	if err != nil {
		return AgentConfig{}, fmt.Errorf("fleet: redact workspace config: %w", err)
	}
	data, err := marshalYAMLDocuments(docs)
	if err != nil {
		return AgentConfig{}, fmt.Errorf("fleet: redact workspace config: %w", err)
	}
	state.Content = string(data)
	return state, nil
}

func mergeRedactedEnvironmentValues(content []byte, current AgentConfig) ([]byte, error) {
	docs, variables, err := parseEnvironmentVariables(content)
	if err != nil || variables == nil {
		return content, err
	}
	needsMerge := false
	escapedLiteral := false
	literalNodes := map[*yaml.Node]struct{}{}
	err = walkEnvironmentVariableValues(variables, func(_ string, value *yaml.Node) error {
		scalar, scalarErr := scalarEnvironmentValue(value)
		if scalarErr != nil {
			return scalarErr
		}
		if _, literal := literalNodes[scalar]; literal {
			return nil
		}
		if scalar.Tag == literalEnvironmentValueYAMLTag {
			if scalar.Value != redactedEnvironmentValue {
				return fmt.Errorf(
					"fleet: %s is only valid for the redacted environment placeholder",
					literalEnvironmentValueYAMLTag,
				)
			}
			scalar.Tag = "!!str"
			scalar.Style = yaml.DoubleQuotedStyle
			literalNodes[scalar] = struct{}{}
			escapedLiteral = true
			return nil
		}
		if scalar.Value == redactedEnvironmentValue {
			needsMerge = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !needsMerge {
		if escapedLiteral {
			return marshalYAMLDocuments(docs)
		}
		return content, nil
	}
	if !current.Exists {
		return nil, fmt.Errorf("fleet: redacted environment placeholder has no existing value")
	}
	_, existingVariables, err := parseEnvironmentVariables([]byte(current.Content))
	if err != nil {
		return nil, err
	}
	existing := map[string][]*yaml.Node{}
	if existingVariables != nil {
		err = walkEnvironmentVariableValues(existingVariables, func(key string, value *yaml.Node) error {
			scalar, scalarErr := scalarEnvironmentValue(value)
			if scalarErr != nil {
				return scalarErr
			}
			existing[key] = append(existing[key], scalar)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	occurrences := map[string]int{}
	err = walkEnvironmentVariableValues(variables, func(key string, value *yaml.Node) error {
		scalar, scalarErr := scalarEnvironmentValue(value)
		if scalarErr != nil {
			return scalarErr
		}
		index := occurrences[key]
		occurrences[key] = index + 1
		if _, literal := literalNodes[scalar]; literal {
			return nil
		}
		if scalar.Value != redactedEnvironmentValue {
			return nil
		}
		values := existing[key]
		if index >= len(values) {
			return fmt.Errorf("fleet: redacted environment placeholder for %q has no existing value", key)
		}
		*scalar = *values[index]
		return nil
	})
	if err != nil {
		return nil, err
	}
	return marshalYAMLDocuments(docs)
}

func walkEnvironmentVariableValues(
	variables *yaml.Node,
	visit func(key string, value *yaml.Node) error,
) error {
	return walkEnvironmentVariableNode(variables, visit, map[*yaml.Node]struct{}{})
}

func walkEnvironmentVariableNode(
	node *yaml.Node,
	visit func(key string, value *yaml.Node) error,
	visited map[*yaml.Node]struct{},
) error {
	if node == nil {
		return fmt.Errorf("environment.variables merge source is empty")
	}
	if node.Kind == yaml.AliasNode {
		if node.Alias == nil {
			return fmt.Errorf("environment.variables merge alias has no target")
		}
		return walkEnvironmentVariableNode(node.Alias, visit, visited)
	}
	if _, ok := visited[node]; ok {
		return nil
	}
	visited[node] = struct{}{}
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			value := node.Content[i+1]
			if key.Tag == "!!merge" || key.Value == "<<" {
				if err := walkEnvironmentVariableNode(value, visit, visited); err != nil {
					return err
				}
				continue
			}
			if err := visit(key.Value, value); err != nil {
				return err
			}
		}
		return nil
	case yaml.SequenceNode:
		for _, item := range node.Content {
			if err := walkEnvironmentVariableNode(item, visit, visited); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("environment.variables merge source must be a mapping or sequence")
	}
}

func scalarEnvironmentValue(node *yaml.Node) (*yaml.Node, error) {
	visited := map[*yaml.Node]struct{}{}
	for node != nil && node.Kind == yaml.AliasNode {
		if _, ok := visited[node]; ok {
			return nil, fmt.Errorf("environment variable alias cycle")
		}
		visited[node] = struct{}{}
		node = node.Alias
	}
	if node == nil || node.Kind != yaml.ScalarNode {
		return nil, fmt.Errorf("environment variable value must be a scalar")
	}
	return node, nil
}

func parseEnvironmentVariables(data []byte) ([]*yaml.Node, *yaml.Node, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var docs []*yaml.Node
	for {
		var doc yaml.Node
		if err := decoder.Decode(&doc); err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, err
		}
		docs = append(docs, &doc)
	}
	if len(docs) == 0 || len(docs[0].Content) == 0 {
		return docs, nil, nil
	}
	root := docs[0].Content[0]
	environmentNode, err := uniqueMappingValue(root, "environment")
	if err != nil {
		return nil, nil, err
	}
	if environmentNode == nil {
		return docs, nil, nil
	}
	variables, err := uniqueMappingValue(environmentNode, "variables")
	if err != nil {
		return nil, nil, err
	}
	if variables != nil && variables.Kind == yaml.ScalarNode && variables.Tag == "!!null" {
		variables = nil
	}
	if variables != nil && variables.Kind != yaml.MappingNode {
		return nil, nil, fmt.Errorf("environment.variables must be a mapping")
	}
	return docs, variables, nil
}

func uniqueMappingValue(node *yaml.Node, key string) (*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil, nil
	}
	var value *yaml.Node
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			if value != nil {
				return nil, fmt.Errorf("duplicate %q mapping key", key)
			}
			value = node.Content[i+1]
		}
	}
	return value, nil
}

func marshalYAMLDocuments(docs []*yaml.Node) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	for _, doc := range docs {
		if err := encoder.Encode(doc); err != nil {
			return nil, err
		}
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
