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
	profile ProviderProfile
	client  openai.Client
}

func NewOpenAIResponses(profile ProviderProfile, _ any) Provider {
	profile = cloneProviderProfile(profile)
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
		profile: profile,
		client:  openai.NewClient(opts...),
	}
}

func (p *openAIResponsesProvider) Name() string { return p.profile.ID + ":" + p.profile.Model }

func (p *openAIResponsesProvider) Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, CompleteOptions{})
}

func (p *openAIResponsesProvider) CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error) {
	providerContext, err := BuildProviderContext(history, p.profile, ProviderContextOptions{})
	if err != nil {
		return Response{}, err
	}
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(p.profile.Model),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: encodeOpenAIResponseInput(providerContext.Messages, p.profile),
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

func encodeOpenAIResponseInput(history []Message, profile ProviderProfile) responses.ResponseInputParam {
	var out responses.ResponseInputParam
	for _, m := range history {
		var textParts []string
		var contentParts responses.ResponseInputMessageContentListParam
		flushTextToContent := func() {
			if len(textParts) == 0 {
				return
			}
			contentParts = append(contentParts, responses.ResponseInputContentParamOfInputText(strings.Join(textParts, "\n")))
			textParts = nil
		}
		flushMessage := func() {
			out = appendResponseMessage(out, m.Role, textParts, contentParts)
			textParts = nil
			contentParts = nil
		}
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockText:
				textParts = append(textParts, b.Text)
			case BlockImage:
				if dataURL, ok := imageDataURL(profile.WorkDir, b.Media); ok {
					flushTextToContent()
					imagePart := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
					imagePart.OfInputImage.ImageURL = param.NewOpt(dataURL)
					contentParts = append(contentParts, imagePart)
				} else {
					textParts = append(textParts, mediaReferenceText("image", b.Media))
				}
			case BlockToolUse:
				flushMessage()
				out = append(out, responses.ResponseInputItemParamOfFunctionCall(toolCallArguments(b.ToolName, b.Input), b.ToolUseID, b.ToolName))
			case BlockToolResult:
				flushMessage()
				content := b.Content
				if b.Media != nil {
					content = toolResultContentWithMediaReference(b)
				}
				out = append(out, responses.ResponseInputItemParamOfFunctionCallOutput(b.ToolUseID, content))
				if b.Media != nil {
					if dataURL, ok := imageDataURL(profile.WorkDir, b.Media); ok {
						imagePart := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
						imagePart.OfInputImage.ImageURL = param.NewOpt(dataURL)
						out = appendResponseMessage(out, RoleUser, nil, responses.ResponseInputMessageContentListParam{
							responses.ResponseInputContentParamOfInputText(openAIToolResultImageAttribution(b)),
							imagePart,
						})
					}
				}
			case BlockReasoning:
				if b.Signature == "" {
					continue
				}
				flushMessage()
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
		flushMessage()
	}
	return out
}

func appendResponseMessage(out responses.ResponseInputParam, role Role, textParts []string, contentParts responses.ResponseInputMessageContentListParam) responses.ResponseInputParam {
	if len(textParts) == 0 && len(contentParts) == 0 {
		return out
	}
	if len(contentParts) == 0 {
		return append(out, responses.ResponseInputItemParamOfMessage(strings.Join(textParts, "\n"), toResponseRole(role)))
	}
	if len(textParts) > 0 {
		contentParts = append(contentParts, responses.ResponseInputContentParamOfInputText(strings.Join(textParts, "\n")))
	}
	return append(out, responses.ResponseInputItemParamOfMessage(contentParts, toResponseRole(role)))
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
