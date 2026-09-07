package llm

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBlockImageJSONRoundTripStoresMediaReference(t *testing.T) {
	original := Message{
		Role: RoleUser,
		Blocks: []Block{{
			Type: BlockImage,
			Media: &MediaRef{
				ArtifactPath:  "threads/123456/media/sha.png",
				MediaType:     "image/png",
				SHA256:        "sha",
				OriginalBytes: 123,
				Width:         8,
				Height:        9,
			},
		}},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "base64") {
		t.Fatalf("message JSON should store media references only: %s", data)
	}
	var roundTrip Message
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if len(roundTrip.Blocks) != 1 || roundTrip.Blocks[0].Type != BlockImage || roundTrip.Blocks[0].Media == nil {
		t.Fatalf("round trip = %+v", roundTrip)
	}
	if roundTrip.Blocks[0].Media.SHA256 != "sha" || roundTrip.Blocks[0].Media.Width != 8 {
		t.Fatalf("media ref = %+v", roundTrip.Blocks[0].Media)
	}
}

func TestProviderContextPreservesRuntimeContextKindBoundary(t *testing.T) {
	runtimeContext := TextMessage(RoleUser, "Current working notes")
	runtimeContext.Kind = MessageKindRuntimeContext
	ctx, err := BuildProviderContext([]Message{
		TextMessage(RoleUser, "durable user input"),
		runtimeContext,
	}, ProviderProfile{Protocol: ProtocolAnthropicMessages, Capabilities: ProviderCapabilities{Tools: true}}, ProviderContextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Messages) != 2 {
		t.Fatalf("messages len = %d, want durable and runtime context split: %+v", len(ctx.Messages), ctx.Messages)
	}
	if ctx.Messages[1].Kind != MessageKindRuntimeContext {
		t.Fatalf("runtime context kind = %q, want %q", ctx.Messages[1].Kind, MessageKindRuntimeContext)
	}
}

func TestIsContextOverflowError(t *testing.T) {
	for _, msg := range []string{
		"openai: context_length_exceeded",
		"maximum context length is 6400 tokens",
		"prompt is too long",
		"input length exceeds context window",
	} {
		if !IsContextOverflowError(fmt.Errorf("wrapped: %s", msg)) {
			t.Fatalf("expected overflow for %q", msg)
		}
	}
	if IsContextOverflowError(fmt.Errorf("rate limit exceeded")) {
		t.Fatal("rate limit should not be classified as context overflow")
	}
}

func TestToolCallArgumentsUsesEmptyObjectForNilInput(t *testing.T) {
	if got := ToolCallArguments("", nil); got != "{}" {
		t.Fatalf("nil arguments = %q, want {}", got)
	}
	if got := ToolCallArguments("", map[string]any{"path": "x"}); got != `{"path":"x"}` {
		t.Fatalf("map arguments = %q", got)
	}
	if got := ToolCallArguments("", map[string]any{"_raw_arguments": `{"path":"x"}`}); got != `{"path":"x"}` {
		t.Fatalf("decoded raw arguments = %q, want structured path", got)
	}
	if got := ToolCallArguments("", map[string]any{"_raw_arguments": `{"path":"unterminated`}); got != "{}" {
		t.Fatalf("malformed raw arguments = %q, want sanitized empty object", got)
	}
	if got := ToolCallArguments("", map[string]any{"_raw_arguments": `null`}); got != "{}" {
		t.Fatalf("null raw arguments = %q, want sanitized empty object", got)
	}
	if got := ToolCallArguments("", map[string]any{"_raw_arguments": `"null"`}); got != "{}" {
		t.Fatalf("encoded null raw arguments = %q, want sanitized empty object", got)
	}
}

func TestBuildProviderContextOwnsProjectionAndValidation(t *testing.T) {
	history := []Message{
		{
			Role: RoleAssistant,
			Blocks: []Block{
				{Type: BlockReasoning, Text: "hidden", Signature: "rs_1"},
				{
					Type:      BlockToolUse,
					ToolUseID: "call_1",
					ToolName:  "read",
					Input:     map[string]any{"_raw_arguments": `{"path":"README.md"}`},
				},
			},
		},
		{
			Role: RoleUser,
			Blocks: []Block{{
				Type:      BlockToolResult,
				ToolUseID: "call_1",
				Content:   "file",
			}},
		},
	}
	providerContext, err := BuildProviderContext(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true, ReasoningReplay: true},
	}, ProviderContextOptions{OmitReasoning: true})
	if err != nil {
		t.Fatalf("BuildProviderContext: %v", err)
	}
	if len(providerContext.Messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(providerContext.Messages))
	}
	blocks := providerContext.Messages[0].Blocks
	if len(blocks) != 1 || blocks[0].Type != BlockToolUse {
		t.Fatalf("assistant blocks = %+v, want only projected tool use", blocks)
	}
	if blocks[0].Input["_raw_arguments"] != nil || blocks[0].Input["path"] != "README.md" {
		t.Fatalf("tool input = %+v, want decoded provider-visible input", blocks[0].Input)
	}
}

func TestBuildProviderContextOmitsPolicyBlockedMessages(t *testing.T) {
	allowed := TextMessage(RoleUser, "allowed input")
	allowed.Kind = MessageKindDirect
	blocked := TextMessage(RoleUser, "sensitive rejected input")
	blocked.Kind = MessageKindDirect
	blocked.PolicyBlocked = true
	answer := TextMessage(RoleAssistant, "answer")

	providerContext, err := BuildProviderContext(
		[]Message{allowed, blocked, answer},
		ProviderProfile{},
		ProviderContextOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(providerContext.Messages) != 2 || providerContext.Messages[0].FirstText() != "allowed input" || providerContext.Messages[1].FirstText() != "answer" {
		t.Fatalf("provider messages = %+v, want policy-blocked input omitted", providerContext.Messages)
	}
}

func TestBuildProviderContextValidatesAfterCapabilityFiltering(t *testing.T) {
	history := []Message{
		{
			Role: RoleAssistant,
			Blocks: []Block{{
				Type:      BlockToolUse,
				ToolUseID: "call_missing",
				ToolName:  "read",
				Input:     map[string]any{"path": "README.md"},
			}},
		},
	}
	if _, err := BuildProviderContext(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, ProviderContextOptions{}); err == nil {
		t.Fatal("expected missing tool_result error when tools are provider-visible")
	}
	providerContext, err := BuildProviderContext(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: false},
	}, ProviderContextOptions{})
	if err != nil {
		t.Fatalf("tool blocks filtered by capability should not fail provider-visible validation: %v", err)
	}
	if len(providerContext.Messages) != 0 {
		t.Fatalf("provider-visible messages = %+v, want empty after filtering and compaction", providerContext.Messages)
	}
}

func TestProjectProviderTranscriptPreservesWriteChunkContent(t *testing.T) {
	history := []Message{{
		Role: RoleAssistant,
		Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "chunk_1",
			ToolName:  "write_chunk",
			Input: map[string]any{
				"write_id": "write-abc",
				"index":    2,
				"content":  strings.Repeat("x", 128),
				"sha256":   "abc123",
			},
		}},
	}}
	projected := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	input := projected[0].Blocks[0].Input
	if input["content"] != strings.Repeat("x", 128) {
		t.Fatalf("projected write_chunk input should keep content: %+v", input)
	}
	if input["write_id"] != "write-abc" || input["index"] != 2 {
		t.Fatalf("projected input lost metadata: %+v", input)
	}
	if history[0].Blocks[0].Input["content"] == nil {
		t.Fatalf("projection mutated original history: %+v", history[0].Blocks[0].Input)
	}
	if input := ProviderToolInput("other_tool", map[string]any{"write_id": "x", "index": 0, "content": "keep"}); input["content"] != "keep" {
		t.Fatalf("non-write_chunk input should keep content: %+v", input)
	}
}

func TestProjectProviderTranscriptGatesToolsAndReasoning(t *testing.T) {
	history := []Message{
		{
			Role: RoleAssistant,
			Blocks: []Block{
				{Type: BlockText, Text: "thinking result"},
				{Type: BlockReasoning, Text: "hidden", Signature: "rs_1"},
				{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "read", Input: map[string]any{"path": "x"}},
			},
		},
		{
			Role: RoleUser,
			Blocks: []Block{
				{Type: BlockText, Text: "continue"},
				{Type: BlockToolResult, ToolUseID: "call_1", Content: "file"},
			},
		},
	}

	noTools := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{ReasoningReplay: true},
	}, providerProjectionOptions{})
	if len(noTools[0].Blocks) != 2 || noTools[0].Blocks[1].Type != BlockReasoning {
		t.Fatalf("assistant blocks without tools = %+v", noTools[0].Blocks)
	}
	if len(noTools[1].Blocks) != 1 || noTools[1].Blocks[0].Type != BlockText {
		t.Fatalf("user blocks without tools = %+v", noTools[1].Blocks)
	}

	noReasoning := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	if len(noReasoning[0].Blocks) != 2 || noReasoning[0].Blocks[1].Type != BlockToolUse {
		t.Fatalf("assistant blocks without reasoning = %+v", noReasoning[0].Blocks)
	}
	if len(noReasoning[1].Blocks) != 2 || noReasoning[1].Blocks[1].Type != BlockToolResult {
		t.Fatalf("user blocks with tools = %+v", noReasoning[1].Blocks)
	}

	omitReasoning := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true, ReasoningReplay: true},
	}, providerProjectionOptions{OmitReasoning: true})
	for _, b := range omitReasoning[0].Blocks {
		if b.Type == BlockReasoning {
			t.Fatalf("reasoning should be omitted for codex-style projection: %+v", omitReasoning[0].Blocks)
		}
	}

	if len(history[0].Blocks) != 3 || history[0].Blocks[1].Type != BlockReasoning {
		t.Fatalf("projection mutated input history: %+v", history[0].Blocks)
	}
}

func TestProjectProviderTranscriptGatesVision(t *testing.T) {
	media := &MediaRef{ArtifactPath: "image.png", MediaType: "image/png", SHA256: "sha", OriginalBytes: 12}
	history := []Message{
		{Role: RoleUser, Blocks: []Block{{Type: BlockImage, Media: media}}},
		{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "render_chart", Input: map[string]any{"id": "x"}}}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "call_1", ToolName: "render_chart", Media: media}}},
	}

	noVision := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	if noVision[0].Blocks[0].Type != BlockText || !strings.Contains(noVision[0].Blocks[0].Text, "image.png") {
		t.Fatalf("image without vision = %+v", noVision[0].Blocks[0])
	}
	for _, want := range []string{"cannot view image content", "instead of guessing"} {
		if !strings.Contains(noVision[0].Blocks[0].Text, want) {
			t.Fatalf("image placeholder missing %q: %q", want, noVision[0].Blocks[0].Text)
		}
	}
	if noVision[2].Blocks[0].Type != BlockToolResult || noVision[2].Blocks[0].Media != nil || !strings.Contains(noVision[2].Blocks[0].Content, "tool_result_image") {
		t.Fatalf("tool result image without vision = %+v", noVision[2].Blocks[0])
	}
	for _, want := range []string{"cannot view image content", "instead of guessing"} {
		if !strings.Contains(noVision[2].Blocks[0].Content, want) {
			t.Fatalf("tool result placeholder missing %q: %q", want, noVision[2].Blocks[0].Content)
		}
	}

	withVision := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true, Vision: true},
	}, providerProjectionOptions{})
	if withVision[0].Blocks[0].Type != BlockImage || withVision[0].Blocks[0].Media == nil {
		t.Fatalf("image with vision = %+v", withVision[0].Blocks[0])
	}
	if withVision[2].Blocks[0].Type != BlockToolResult || withVision[2].Blocks[0].Media == nil {
		t.Fatalf("tool result image with vision = %+v", withVision[2].Blocks[0])
	}
}

func TestProjectProviderTranscriptCompactsAfterFiltering(t *testing.T) {
	history := []Message{
		{
			Role:   RoleUser,
			Blocks: []Block{{Type: BlockText, Text: "first"}},
		},
		{
			Role: RoleAssistant,
			Blocks: []Block{{
				Type:      BlockToolUse,
				ToolUseID: "call_1",
				ToolName:  "read",
				Input:     map[string]any{"path": "x"},
			}},
		},
		{
			Role:   RoleUser,
			Blocks: []Block{{Type: BlockText, Text: "second"}},
		},
	}

	projected := projectProviderTranscript(history, ProviderProfile{}, providerProjectionOptions{})
	if len(projected) != 1 {
		t.Fatalf("projected message count = %d, want 1: %+v", len(projected), projected)
	}
	if projected[0].Role != RoleUser {
		t.Fatalf("projected role = %q, want user", projected[0].Role)
	}
	if len(projected[0].Blocks) != 2 {
		t.Fatalf("projected user block count = %d, want 2: %+v", len(projected[0].Blocks), projected[0].Blocks)
	}
	if got := []string{projected[0].Blocks[0].Text, projected[0].Blocks[1].Text}; got[0] != "first" || got[1] != "second" {
		t.Fatalf("projected user blocks = %+v, want first/second", projected[0].Blocks)
	}
}

func TestNormalizedFunctionParametersDefaults(t *testing.T) {
	schema := NormalizedFunctionParameters(map[string]any{"required": []any{"path", 3}})
	llmAssertObjectWithEmptyProperties(t, schema)
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("required-only schema should stay open, got %+v", schema)
	}
	req := NormalizedFunctionRequired(schema)
	if len(req) != 1 || req[0] != "path" {
		t.Fatalf("required = %+v, want [path]", req)
	}
	noArgs := NormalizedFunctionParameters(map[string]any{"type": "object"})
	llmAssertEmptyProperties(t, noArgs)
	props := NormalizedFunctionProperties(map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}})
	if _, ok := props["path"]; !ok {
		t.Fatalf("properties = %+v, want path", props)
	}
}

func TestNormalizedFunctionParametersKeepsExplicitOpenEmptyObject(t *testing.T) {
	schema := NormalizedFunctionParameters(map[string]any{"type": "object", "properties": map[string]any{}})
	llmAssertObjectWithEmptyProperties(t, schema)
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("explicit empty properties schema should stay open, got %+v", schema)
	}
}

func TestNormalizedFunctionParametersPreservesExplicitAdditionalProperties(t *testing.T) {
	schema := NormalizedFunctionParameters(map[string]any{"type": "object", "additionalProperties": true})
	llmAssertObjectWithEmptyProperties(t, schema)
	if schema["additionalProperties"] != true {
		t.Fatalf("additionalProperties = %+v, want true", schema["additionalProperties"])
	}
}

func TestNormalizedFunctionParametersFlattensComposedObjectProperties(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"source": map[string]any{
				"type": "object",
				"oneOf": []map[string]any{
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"type":    map[string]any{"type": "string", "enum": []any{"command"}},
							"command": map[string]any{"type": "string"},
							"filters": map[string]any{
								"type": "array",
								"items": map[string]any{
									"type":                 "object",
									"additionalProperties": false,
									"properties": map[string]any{
										"contains": map[string]any{"type": "string"},
										"regex":    map[string]any{"type": "string"},
									},
									"oneOf": []map[string]any{
										map[string]any{"required": []any{"contains"}},
										map[string]any{"required": []any{"regex"}},
									},
								},
							},
						},
					},
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"type":     map[string]any{"type": "string", "enum": []any{"schedule"}},
							"interval": map[string]any{"type": "object"},
						},
						"oneOf": []map[string]any{
							map[string]any{"required": []any{"interval"}},
						},
					},
				},
			},
		},
	}

	normalized := NormalizedFunctionParameters(input)
	source := llmSchemaValueMap(t, llmSchemaValueMap(t, normalized["properties"])["source"])
	if _, ok := source["oneOf"]; ok {
		t.Fatalf("source oneOf should be flattened for provider schemas: %+v", source)
	}
	sourceProps := llmSchemaValueMap(t, source["properties"])
	for _, want := range []string{"type", "command", "filters", "interval"} {
		if _, ok := sourceProps[want]; !ok {
			t.Fatalf("source properties missing %q: %+v", want, sourceProps)
		}
	}
	typeEnum, ok := llmSchemaValueMap(t, sourceProps["type"])["enum"].([]any)
	if !ok || len(typeEnum) != 2 || typeEnum[0] != "command" || typeEnum[1] != "schedule" {
		t.Fatalf("source type enum = %#v, want command and schedule", llmSchemaValueMap(t, sourceProps["type"])["enum"])
	}
	filterItems := llmSchemaValueMap(t, llmSchemaValueMap(t, sourceProps["filters"])["items"])
	if filterItems["type"] != "object" {
		t.Fatalf("filter item type = %v, want object in %+v", filterItems["type"], filterItems)
	}
	if _, ok := filterItems["oneOf"]; ok {
		t.Fatalf("filter item oneOf should be dropped for provider schemas: %+v", filterItems)
	}
	filterProps := llmSchemaValueMap(t, filterItems["properties"])
	for _, want := range []string{"contains", "regex"} {
		if _, ok := filterProps[want]; !ok {
			t.Fatalf("filter properties missing %q: %+v", want, filterProps)
		}
	}
	originalSource := llmSchemaValueMap(t, llmSchemaValueMap(t, input["properties"])["source"])
	if _, ok := originalSource["oneOf"]; !ok {
		t.Fatalf("normalization mutated input schema: %+v", input)
	}
}

func TestNormalizedFunctionParametersKeepsRefSchemasOpen(t *testing.T) {
	schema := NormalizedFunctionParameters(map[string]any{
		"type": "object",
		"$ref": "#/$defs/query",
		"$defs": map[string]any{
			"query": map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
				"required":   []any{"query"},
			},
		},
	})
	llmAssertObjectWithEmptyProperties(t, schema)
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("ref schema should stay open, got %+v", schema)
	}
}

func TestNormalizedFunctionParametersKeepsPropertyCountSchemasOpen(t *testing.T) {
	schema := NormalizedFunctionParameters(map[string]any{"type": "object", "minProperties": 1})
	llmAssertObjectWithEmptyProperties(t, schema)
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("property-count schema should stay open, got %+v", schema)
	}
}

func TestParseToolArguments(t *testing.T) {
	input := ParseToolArguments(`{"path":"x","content":"hello"}`)
	if input["path"] != "x" || input["content"] != "hello" {
		t.Fatalf("input = %+v, want parsed object", input)
	}

	doubleEncoded := ParseToolArguments(`"{\"path\":\"x\",\"content\":\"hello\"}"`)
	if doubleEncoded["path"] != "x" || doubleEncoded["content"] != "hello" {
		t.Fatalf("doubleEncoded = %+v, want parsed object", doubleEncoded)
	}

	raw := ParseToolArguments(`{"path":`)
	if raw["_raw_arguments"] != `{"path":` {
		t.Fatalf("raw = %+v, want preserved raw arguments", raw)
	}
}

func llmAssertEmptyProperties(t *testing.T, schema map[string]any) {
	t.Helper()
	llmAssertObjectWithEmptyProperties(t, schema)
	if schema["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %+v, want false in empty object schema %+v", schema["additionalProperties"], schema)
	}
}

func llmAssertObjectWithEmptyProperties(t *testing.T, schema map[string]any) {
	t.Helper()
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v, want object in %+v", schema["type"], schema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || props == nil {
		t.Fatalf("properties should be an empty object, got %+v in schema %+v", schema["properties"], schema)
	}
	if len(props) != 0 {
		t.Fatalf("properties = %+v, want empty object", props)
	}
}

func llmSchemaValueMap(t *testing.T, value any) map[string]any {
	t.Helper()
	out, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("schema value = %#v, want map[string]any", value)
	}
	return out
}
