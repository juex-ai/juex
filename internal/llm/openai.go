package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
)

// openAIProvider wraps the official openai-go client. The same client targets
// every OpenAI-compatible endpoint (DeepSeek, OpenRouter, Ollama, ...) by
// swapping option.WithBaseURL.
type openAIProvider struct {
	cfg     Config
	profile ProviderProfile
	client  openai.Client
}

func NewOpenAI(cfg Config, _ any) Provider {
	profile, err := ResolveProfile(cfg)
	if err != nil {
		profile = customOpenAIChatProfile(firstNonEmpty(cfg.ID, "openai"), ProtocolOpenAIChat)
		profile.APIKey = cfg.APIKey
		profile.Model = cfg.Model
		profile.BaseURL = cfg.BaseURL
		profile.ThinkingEffort = cfg.ThinkingEffort
		profile.Headers = cloneStringMap(cfg.Headers)
		profile.Query = cloneStringMap(cfg.Query)
		profile.Capabilities = applyCapabilityOverrides(profile.Capabilities, cfg.Capabilities)
		if len(cfg.Compat.ReasoningReplayFields) > 0 {
			profile.Compat = cfg.Compat
		}
	}
	opts := []option.RequestOption{
		option.WithAPIKey(profile.APIKey),
		option.WithMaxRetries(providerMaxRetries),
	}
	if profile.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(profile.BaseURL))
	}
	for k, v := range profile.Headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	for k, v := range profile.Query {
		opts = append(opts, option.WithQuery(k, v))
	}
	return &openAIProvider{
		cfg:     profile.Config(),
		profile: profile,
		client:  openai.NewClient(opts...),
	}
}

func (p *openAIProvider) Name() string { return p.profile.ID + ":" + p.profile.Model }

func (p *openAIProvider) Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, CompleteOptions{})
}

func (p *openAIProvider) CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error) {
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(history)+1)
	if sys != "" {
		msgs = append(msgs, openai.SystemMessage(sys))
	}
	msgs = append(msgs, toOpenAIMessages(history, p.profile)...)

	params := openai.ChatCompletionNewParams{
		Model:    p.profile.Model,
		Messages: msgs,
	}
	if p.profile.Capabilities.Tools {
		params.Tools = toOpenAITools(tools)
	}
	if p.profile.Capabilities.ReasoningEffort && p.profile.ThinkingEffort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(p.profile.ThinkingEffort)
	}
	if p.profile.Capabilities.MaxOutputTokens && opts.MaxOutputTokens > 0 {
		params.MaxCompletionTokens = openai.Int(int64(opts.MaxOutputTokens))
	}
	if opts.CachePolicy.StablePrefixKey != "" {
		params.PromptCacheKey = openai.String(opts.CachePolicy.StablePrefixKey)
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("openai: %w", err)
	}
	if len(completion.Choices) == 0 {
		return Response{}, fmt.Errorf("openai: empty choices")
	}
	choice := completion.Choices[0]

	out := Message{Role: RoleAssistant, Model: p.Name()}
	// DeepSeek and similar providers attach `reasoning_content` to the
	// assistant message. We surface it as a Block so it round-trips on the
	// next call (DeepSeek rejects requests that omit it after a thinking
	// turn).
	if rc := extractReasoningContent(choice.Message.RawJSON()); rc != "" {
		out.Blocks = append(out.Blocks, Block{Type: BlockReasoning, Text: rc})
	}
	if choice.Message.Content != "" {
		out.Blocks = append(out.Blocks, Block{Type: BlockText, Text: choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		out.Blocks = append(out.Blocks, Block{
			Type:      BlockToolUse,
			ToolUseID: tc.ID,
			ToolName:  tc.Function.Name,
			Input:     parseToolArguments(tc.Function.Arguments),
		})
	}

	return Response{
		Message:    out,
		StopReason: mapOpenAIStop(string(choice.FinishReason)),
		Usage: Usage{
			InputTokens:       int(completion.Usage.PromptTokens),
			OutputTokens:      int(completion.Usage.CompletionTokens),
			CachedInputTokens: int(completion.Usage.PromptTokensDetails.CachedTokens),
		},
	}, nil
}

// toOpenAIMessages converts Juex history into OpenAI-shaped messages,
// splitting user-role tool_result blocks into role=tool messages so the
// tool_call_id <-> tool message linkage is preserved.
func toOpenAIMessages(history []Message, profile ProviderProfile) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	for _, m := range compactHistoryForProvider(history) {
		switch m.Role {
		case RoleUser:
			var userText strings.Builder
			for _, b := range m.Blocks {
				switch b.Type {
				case BlockText:
					if userText.Len() > 0 {
						userText.WriteString("\n")
					}
					userText.WriteString(b.Text)
				case BlockToolResult:
					if !profile.Capabilities.Tools {
						continue
					}
					if userText.Len() > 0 {
						out = append(out, openai.UserMessage(userText.String()))
						userText.Reset()
					}
					out = append(out, openai.ToolMessage(b.Content, b.ToolUseID))
				}
			}
			if userText.Len() > 0 {
				out = append(out, openai.UserMessage(userText.String()))
			}
		case RoleAssistant:
			am := openai.ChatCompletionAssistantMessageParam{}
			var textParts []string
			var reasoningParts []string
			var toolCalls []openai.ChatCompletionMessageToolCallParam
			for _, b := range m.Blocks {
				switch b.Type {
				case BlockText:
					textParts = append(textParts, b.Text)
				case BlockReasoning:
					if profile.Capabilities.ReasoningReplay {
						reasoningParts = append(reasoningParts, b.Text)
					}
				case BlockToolUse:
					if !profile.Capabilities.Tools {
						continue
					}
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallParam{
						ID: b.ToolUseID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      b.ToolName,
							Arguments: toolCallArguments(b.Input),
						},
					})
				}
			}
			if len(textParts) > 0 {
				am.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: param.NewOpt(strings.Join(textParts, "\n")),
				}
			}
			am.ToolCalls = toolCalls
			if len(reasoningParts) > 0 {
				joined := strings.Join(reasoningParts, "\n")
				extra := map[string]any{}
				for _, field := range profile.Compat.ReasoningReplayFields {
					extra[field] = joined
				}
				if len(extra) > 0 {
					am.SetExtraFields(extra)
				}
			}
			out = append(out, openai.ChatCompletionMessageParamUnion{OfAssistant: &am})
		case RoleSystem:
			if text := m.FirstText(); text != "" {
				out = append(out, openai.SystemMessage(text))
			}
		}
	}
	return out
}

func toOpenAITools(tools []ToolSpec) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: param.NewOpt(t.Description),
				Parameters:  shared.FunctionParameters(normalizedFunctionParameters(t.Schema)),
			},
		})
	}
	return out
}

func normalizedFunctionParameters(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema)+2)
	for k, v := range schema {
		out[k] = v
	}
	if out["type"] == nil || out["type"] == "" {
		out["type"] = "object"
	}
	if out["properties"] == nil {
		out["properties"] = map[string]any{}
	}
	return out
}

func toolCallArguments(input map[string]any) string {
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
	var input map[string]any
	if err := json.Unmarshal([]byte(raw), &input); err == nil {
		return input
	}
	var encoded string
	if err := json.Unmarshal([]byte(raw), &encoded); err == nil {
		if err := json.Unmarshal([]byte(encoded), &input); err == nil {
			return input
		}
		return map[string]any{"_raw_arguments": encoded}
	}
	return map[string]any{"_raw_arguments": raw}
}

// extractReasoningContent pulls a reasoning/thinking string out of a raw
// assistant message payload. Field names vary by provider:
//   - DeepSeek and most "OpenAI-compatible" thinking surfaces use `reasoning_content`.
//   - Ollama's OpenAI shim uses `reasoning`.
//   - Some other shims have surfaced `thinking`.
//
// We try them in that order and return the first non-empty value.
func extractReasoningContent(raw string) string {
	if raw == "" {
		return ""
	}
	var probe struct {
		ReasoningContent string `json:"reasoning_content"`
		Reasoning        string `json:"reasoning"`
		Thinking         string `json:"thinking"`
	}
	_ = json.Unmarshal([]byte(raw), &probe)
	switch {
	case probe.ReasoningContent != "":
		return probe.ReasoningContent
	case probe.Reasoning != "":
		return probe.Reasoning
	default:
		return probe.Thinking
	}
}

func mapOpenAIStop(s string) StopReason {
	switch s {
	case "stop":
		return StopEndTurn
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	default:
		return StopOther
	}
}
