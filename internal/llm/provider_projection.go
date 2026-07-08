package llm

import "encoding/json"

type providerProjectionOptions struct {
	// Codex Responses uses store=false. Replaying reasoning item IDs in that
	// mode makes the backend look up non-persisted items.
	OmitReasoning bool
}

type ProviderContextOptions struct {
	// OmitReasoning removes replayed reasoning blocks from provider-visible
	// history for protocols that cannot reference stored reasoning items.
	OmitReasoning bool
}

type ProviderContext struct {
	Messages []Message
}

func BuildProviderContext(history []Message, profile ProviderProfile, opts ProviderContextOptions) (ProviderContext, error) {
	projected := projectProviderTranscript(history, profile, providerProjectionOptions(opts))
	if err := ValidateToolTranscript(projected); err != nil {
		return ProviderContext{}, err
	}
	return ProviderContext{Messages: projected}, nil
}

func projectProviderTranscript(history []Message, profile ProviderProfile, opts providerProjectionOptions) []Message {
	filtered := make([]Message, 0, len(history))
	for _, m := range history {
		projected := m
		projected.Blocks = make([]Block, 0, len(m.Blocks))
		for _, b := range m.Blocks {
			if shouldProjectProviderBlock(b, profile, opts) {
				projected.Blocks = append(projected.Blocks, projectProviderBlock(b))
			}
		}
		filtered = append(filtered, projected)
	}
	return compactHistoryForProvider(foldChunkedWriteHistoryForProvider(filtered))
}

func shouldProjectProviderBlock(b Block, profile ProviderProfile, opts providerProjectionOptions) bool {
	switch b.Type {
	case BlockText:
		return true
	case BlockToolUse, BlockToolResult:
		return profile.Capabilities.Tools
	case BlockReasoning:
		return !opts.OmitReasoning && profile.Capabilities.ReasoningReplay
	default:
		return false
	}
}

func projectProviderBlock(b Block) Block {
	if b.Type == BlockToolUse {
		b.Input = ProviderToolInput(b.ToolName, b.Input)
	}
	return b
}

func normalizedFunctionParameters(schema map[string]any) map[string]any {
	out := normalizeFunctionSchemaObject(schema)
	if out["type"] == nil || out["type"] == "" {
		out["type"] = "object"
	}
	if out["properties"] == nil {
		out["properties"] = map[string]any{}
	}
	if shouldCloseImplicitNoArgumentSchema(schema, out) {
		out["additionalProperties"] = false
	}
	return out
}

// normalizeFunctionSchemaObject projects rich JSON Schema into the conservative
// subset accepted by OpenAI-compatible tool APIs and Gemini-backed proxies.
// Runtime tool handlers still validate the full semantic contract.
func normalizeFunctionSchemaObject(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema)+2)
	var compositions []any
	for k, v := range schema {
		switch k {
		case "oneOf", "anyOf", "allOf":
			compositions = append(compositions, v)
		case "properties":
			out[k] = normalizeFunctionSchemaPropertiesValue(v)
		default:
			out[k] = normalizeFunctionSchemaValue(v)
		}
	}
	for _, composition := range compositions {
		mergeFunctionSchemaComposition(out, composition)
	}
	return out
}

func normalizeFunctionSchemaPropertiesValue(value any) any {
	props, ok := value.(map[string]any)
	if !ok {
		return normalizeFunctionSchemaValue(value)
	}
	out := make(map[string]any, len(props))
	for name, prop := range props {
		out[name] = normalizeFunctionSchemaValue(prop)
	}
	return out
}

func normalizeFunctionSchemaValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return normalizeFunctionSchemaObject(v)
	case []any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, normalizeFunctionSchemaValue(item))
		}
		return out
	case []map[string]any:
		out := make([]any, 0, len(v))
		for _, item := range v {
			out = append(out, normalizeFunctionSchemaValue(item))
		}
		return out
	default:
		return value
	}
}

func mergeFunctionSchemaComposition(out map[string]any, composition any) {
	var branches []any
	switch c := composition.(type) {
	case []any:
		branches = c
	case []map[string]any:
		branches = make([]any, 0, len(c))
		for _, branch := range c {
			branches = append(branches, branch)
		}
	default:
		return
	}
	for _, branch := range branches {
		branchSchema, ok := branch.(map[string]any)
		if !ok {
			continue
		}
		mergeFunctionSchemaObjectBranch(out, normalizeFunctionSchemaObject(branchSchema))
	}
}

func mergeFunctionSchemaObjectBranch(out, branch map[string]any) {
	branchProps, ok := branch["properties"].(map[string]any)
	if !ok || len(branchProps) == 0 {
		return
	}
	if out["type"] == nil && branch["type"] == "object" {
		out["type"] = "object"
	}
	outProps, ok := out["properties"].(map[string]any)
	if !ok || outProps == nil {
		outProps = map[string]any{}
		out["properties"] = outProps
	}
	for name, prop := range branchProps {
		if _, exists := outProps[name]; exists {
			continue
		}
		outProps[name] = prop
	}
}

func shouldCloseImplicitNoArgumentSchema(original, normalized map[string]any) bool {
	if _, ok := normalized["additionalProperties"]; ok {
		return false
	}
	if _, ok := original["properties"]; ok {
		return false
	}
	props, ok := normalized["properties"].(map[string]any)
	if !ok || len(props) != 0 {
		return false
	}
	if len(functionRequired(normalized)) != 0 {
		return false
	}
	for key := range original {
		if !isNoArgumentSchemaMetadata(key) {
			return false
		}
	}
	return true
}

func isNoArgumentSchemaMetadata(key string) bool {
	switch key {
	case "type", "title", "description", "$schema", "$id", "default", "examples", "deprecated", "readOnly", "writeOnly":
		return true
	default:
		return false
	}
}

func normalizedFunctionProperties(schema map[string]any) map[string]any {
	normalized := normalizedFunctionParameters(schema)
	props, ok := normalized["properties"].(map[string]any)
	if !ok || props == nil {
		return map[string]any{}
	}
	return props
}

func normalizedFunctionRequired(schema map[string]any) []string {
	normalized := normalizedFunctionParameters(schema)
	return functionRequired(normalized)
}

func functionRequired(schema map[string]any) []string {
	switch req := schema["required"].(type) {
	case []string:
		return append([]string(nil), req...)
	case []any:
		out := make([]string, 0, len(req))
		for _, r := range req {
			if s, ok := r.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func toolCallArguments(toolName string, input map[string]any) string {
	input = ProviderToolInput(toolName, input)
	if input == nil {
		return "{}"
	}
	argBytes, err := json.Marshal(input)
	if err != nil {
		return "{}"
	}
	return string(argBytes)
}

func parseToolArguments(raw string) map[string]any {
	if raw == "" {
		return nil
	}
	input, fallback, ok := decodeToolArguments(raw)
	if ok {
		return input
	}
	return map[string]any{"_raw_arguments": fallback}
}

// ProviderToolInput returns the model-visible representation of one tool-use
// input. It normalizes raw JSON arguments while preserving the semantic shape
// of prior tool calls. Required tool arguments must remain visible in replay;
// otherwise models can mistake history compaction for a failed or malformed
// call and abandon the tool flow.
func ProviderToolInput(toolName string, input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	raw, ok := input["_raw_arguments"].(string)
	if !ok || raw == "" {
		return input
	}
	decoded, _, ok := decodeToolArguments(raw)
	if !ok {
		return copyToolInputWithoutRawArguments(input)
	}
	out := make(map[string]any, len(decoded)+len(input))
	for k, v := range decoded {
		out[k] = v
	}
	for k, v := range input {
		if k == "_raw_arguments" {
			continue
		}
		out[k] = v
	}
	return out
}

func copyToolInputWithoutRawArguments(input map[string]any) map[string]any {
	out := make(map[string]any, len(input))
	for k, v := range input {
		if k == "_raw_arguments" {
			continue
		}
		out[k] = v
	}
	return out
}

func decodeToolArguments(raw string) (map[string]any, string, bool) {
	rawBytes := []byte(raw)
	var input map[string]any
	if err := json.Unmarshal(rawBytes, &input); err == nil && input != nil {
		return input, "", true
	}
	var encoded string
	if err := json.Unmarshal(rawBytes, &encoded); err == nil {
		if err := json.Unmarshal([]byte(encoded), &input); err == nil && input != nil {
			return input, "", true
		}
		return nil, encoded, false
	}
	return nil, raw, false
}
