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
	cfg    Config
	client openai.Client
}

func NewOpenAI(cfg Config, _ any) Provider {
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &openAIProvider{
		cfg:    cfg,
		client: openai.NewClient(opts...),
	}
}

func (p *openAIProvider) Name() string { return "openai:" + p.cfg.Model }

func (p *openAIProvider) Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error) {
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(history)+1)
	if sys != "" {
		msgs = append(msgs, openai.SystemMessage(sys))
	}
	msgs = append(msgs, toOpenAIMessages(history)...)

	params := openai.ChatCompletionNewParams{
		Model:    p.cfg.Model,
		Messages: msgs,
		Tools:    toOpenAITools(tools),
	}
	if p.cfg.ThinkingEffort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(p.cfg.ThinkingEffort)
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
		var input map[string]any
		if tc.Function.Arguments != "" {
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
				input = map[string]any{"_raw_arguments": tc.Function.Arguments}
			}
		}
		out.Blocks = append(out.Blocks, Block{
			Type:      BlockToolUse,
			ToolUseID: tc.ID,
			ToolName:  tc.Function.Name,
			Input:     input,
		})
	}

	return Response{
		Message:    out,
		StopReason: mapOpenAIStop(string(choice.FinishReason)),
		Usage: Usage{
			InputTokens:  int(completion.Usage.PromptTokens),
			OutputTokens: int(completion.Usage.CompletionTokens),
		},
	}, nil
}

// toOpenAIMessages converts Juex history into OpenAI-shaped messages,
// splitting user-role tool_result blocks into role=tool messages so the
// tool_call_id <-> tool message linkage is preserved.
func toOpenAIMessages(history []Message) []openai.ChatCompletionMessageParamUnion {
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
					reasoningParts = append(reasoningParts, b.Text)
				case BlockToolUse:
					argBytes, _ := json.Marshal(b.Input)
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallParam{
						ID: b.ToolUseID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      b.ToolName,
							Arguments: string(argBytes),
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
				// Set every known field name. Providers that don't recognise a
				// given field ignore it; providers that require theirs (DeepSeek
				// rejects requests that omit `reasoning_content` after a thinking
				// turn) still get what they need.
				am.SetExtraFields(map[string]any{
					"reasoning_content": joined,
					"reasoning":         joined,
					"thinking":          joined,
				})
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
				Parameters:  shared.FunctionParameters(t.Schema),
			},
		})
	}
	return out
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
