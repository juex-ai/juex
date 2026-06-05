package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

type openAIResponsesProvider struct {
	cfg     Config
	profile ProviderProfile
	client  openai.Client
}

func NewOpenAIResponses(cfg Config, _ any) Provider {
	profile, err := ResolveProfile(cfg)
	if err != nil {
		profile = presetProfile("openai")
		profile.Protocol = ProtocolOpenAIResponses
		profile.APIKey = cfg.APIKey
		profile.Model = cfg.Model
		profile.BaseURL = cfg.BaseURL
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
	return &openAIResponsesProvider{
		cfg:     profile.Config(),
		profile: profile,
		client:  openai.NewClient(opts...),
	}
}

func (p *openAIResponsesProvider) Name() string { return p.profile.ID + ":" + p.profile.Model }

func (p *openAIResponsesProvider) Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, CompleteOptions{})
}

func (p *openAIResponsesProvider) CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error) {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(p.profile.Model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: toOpenAIResponseInput(history, p.profile),
		},
		Store: param.NewOpt(false),
	}
	if sys != "" {
		params.Instructions = param.NewOpt(sys)
	}
	if p.profile.Capabilities.Tools {
		params.Tools = toOpenAIResponseTools(tools)
		if len(params.Tools) > 0 {
			params.ParallelToolCalls = param.NewOpt(true)
		}
	}
	if p.profile.Capabilities.MaxOutputTokens && opts.MaxOutputTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(opts.MaxOutputTokens))
	}
	if p.profile.Capabilities.ReasoningEffort && p.profile.ThinkingEffort != "" {
		params.Reasoning = shared.ReasoningParam{Effort: shared.ReasoningEffort(p.profile.ThinkingEffort)}
	}
	if p.profile.Capabilities.ReasoningReplay {
		params.Include = []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent}
	}
	if opts.CachePolicy.StablePrefixKey != "" {
		params.PromptCacheKey = param.NewOpt(opts.CachePolicy.StablePrefixKey)
	}

	resp, err := p.client.Responses.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("openai responses: %w", err)
	}
	return p.responseFromResponses(resp), nil
}

func (p *openAIResponsesProvider) responseFromResponses(resp *responses.Response) Response {
	out := Message{Role: RoleAssistant, Model: p.Name()}
	stop := StopEndTurn
	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			var summaries []string
			for _, summary := range item.Summary {
				if summary.Text != "" {
					summaries = append(summaries, summary.Text)
				}
			}
			out.Blocks = append(out.Blocks, Block{
				Type:      BlockReasoning,
				Text:      strings.Join(summaries, "\n"),
				Signature: item.ID,
				Content:   item.EncryptedContent,
				Redacted:  item.EncryptedContent != "",
			})
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "output_text":
					out.Blocks = append(out.Blocks, Block{Type: BlockText, Text: c.Text})
				case "refusal":
					out.Blocks = append(out.Blocks, Block{Type: BlockText, Text: c.Refusal})
				}
			}
		case "function_call":
			stop = StopToolUse
			out.Blocks = append(out.Blocks, Block{
				Type:      BlockToolUse,
				ToolUseID: item.CallID,
				ToolName:  item.Name,
				Input:     parseToolArguments(item.Arguments),
			})
		}
	}
	if string(resp.Status) == "incomplete" && resp.IncompleteDetails.Reason == "max_output_tokens" {
		stop = StopMaxTokens
	}
	return Response{
		Message:    out,
		StopReason: stop,
		Usage: Usage{
			InputTokens:       int(resp.Usage.InputTokens),
			OutputTokens:      int(resp.Usage.OutputTokens),
			CachedInputTokens: int(resp.Usage.InputTokensDetails.CachedTokens),
		},
	}
}

func toOpenAIResponseInput(history []Message, profile ProviderProfile) responses.ResponseInputParam {
	var out responses.ResponseInputParam
	for _, m := range compactHistoryForProvider(history) {
		var textParts []string
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockText:
				textParts = append(textParts, b.Text)
			case BlockToolUse:
				if !profile.Capabilities.Tools {
					continue
				}
				out = appendResponseTextMessage(out, m.Role, textParts)
				textParts = nil
				out = append(out, responses.ResponseInputItemParamOfFunctionCall(toolCallArguments(b.Input), b.ToolUseID, b.ToolName))
			case BlockToolResult:
				if !profile.Capabilities.Tools {
					continue
				}
				out = appendResponseTextMessage(out, m.Role, textParts)
				textParts = nil
				out = append(out, responses.ResponseInputItemParamOfFunctionCallOutput(b.ToolUseID, b.Content))
			case BlockReasoning:
				if !profile.Capabilities.ReasoningReplay || b.Signature == "" {
					continue
				}
				out = appendResponseTextMessage(out, m.Role, textParts)
				textParts = nil
				var summary []responses.ResponseReasoningItemSummaryParam
				if b.Text != "" {
					summary = []responses.ResponseReasoningItemSummaryParam{{Text: b.Text}}
				} else {
					summary = []responses.ResponseReasoningItemSummaryParam{}
				}
				reasoning := responses.ResponseReasoningItemParam{
					ID:      b.Signature,
					Summary: summary,
				}
				if b.Content != "" {
					reasoning.EncryptedContent = param.NewOpt(b.Content)
				}
				out = append(out, responses.ResponseInputItemUnionParam{OfReasoning: &reasoning})
			}
		}
		out = appendResponseTextMessage(out, m.Role, textParts)
	}
	return out
}

func appendResponseTextMessage(out responses.ResponseInputParam, role Role, parts []string) responses.ResponseInputParam {
	if len(parts) == 0 {
		return out
	}
	return append(out, responses.ResponseInputItemUnionParam{OfMessage: &responses.EasyInputMessageParam{
		Role:    toResponseRole(role),
		Content: responses.EasyInputMessageContentUnionParam{OfString: param.NewOpt(strings.Join(parts, "\n"))},
	}})
}

func toResponseRole(role Role) responses.EasyInputMessageRole {
	switch role {
	case RoleAssistant:
		return responses.EasyInputMessageRoleAssistant
	case RoleSystem:
		return responses.EasyInputMessageRoleSystem
	default:
		return responses.EasyInputMessageRoleUser
	}
}

func toOpenAIResponseTools(tools []ToolSpec) []responses.ToolUnionParam {
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        t.Name,
				Description: param.NewOpt(t.Description),
				Parameters:  normalizedFunctionParameters(t.Schema),
				Strict:      param.NewOpt(false),
			},
		})
	}
	return out
}
