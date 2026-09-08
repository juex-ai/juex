package openai

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	providermedia "github.com/juex-ai/juex/internal/providers/internal/media"
	protocolsupport "github.com/juex-ai/juex/internal/providers/internal/protocol"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

const (
	openAIResponsesToolCallIDMaxLength  = 64
	openAIResponsesToolCallIDHashPrefix = "juex_"
	openAIResponsesIdleMaxAttempts      = 2
	openAIResponsesRetryBaseDelay       = 100 * time.Millisecond
)

type openAIResponsesProvider struct {
	profile llm.ProviderProfile
	client  openai.Client
}

func NewOpenAIResponses(profile llm.ProviderProfile, _ any) llm.Provider {
	profile = providerprofile.CloneProviderProfile(profile)
	opts := []option.RequestOption{
		option.WithAPIKey(profile.APIKey),
		option.WithMaxRetries(protocolsupport.ProviderMaxRetries),
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

func (p *openAIResponsesProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, llm.CompleteOptions{})
}

func (p *openAIResponsesProvider) CompleteWithOptions(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec, opts llm.CompleteOptions) (result llm.Response, err error) {
	defer func() { err = protocolsupport.WrapProviderError(err) }()
	providerContext, err := llm.BuildProviderContext(history, p.profile, llm.ProviderContextOptions{})
	if err != nil {
		return llm.Response{}, err
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
	if effort := protocolsupport.RequestThinkingEffort(p.profile, opts); p.profile.Capabilities.ReasoningEffort && effort != "" {
		params.Reasoning = shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(effort),
			Summary: shared.ReasoningSummaryAuto,
		}
	}
	if p.profile.Capabilities.ReasoningReplay {
		params.Include = []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent}
	}
	if opts.CachePolicy.StablePrefixKey != "" {
		params.PromptCacheKey = param.NewOpt(opts.CachePolicy.StablePrefixKey)
	}
	if p.profile.Capabilities.Streaming {
		return p.completeStreaming(ctx, params, opts)
	}

	resp, err := p.client.Responses.New(ctx, params)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openai responses: %w", err)
	}
	return p.responseFromResponses(resp), nil
}

func (p *openAIResponsesProvider) completeStreaming(ctx context.Context, params responses.ResponseNewParams, opts llm.CompleteOptions) (llm.Response, error) {
	idleTimeout := protocolsupport.StreamIdleTimeout(opts)
	for attempt := 1; ; attempt++ {
		resp, err, idleExpired := p.completeStreamingAttempt(ctx, params, opts, idleTimeout)
		if err == nil {
			return resp, nil
		}
		if !idleExpired {
			return llm.Response{}, err
		}
		if ctx.Err() != nil {
			return llm.Response{}, ctx.Err()
		}
		idleErr := protocolsupport.NewStreamIdleTimeoutError("openai responses stream", idleTimeout, err)
		if attempt >= openAIResponsesIdleMaxAttempts {
			p.emitOpenAIResponsesRetryDiagnostic(opts, idleErr, attempt, 0, false, true)
			return llm.Response{}, fmt.Errorf("openai responses stream retry exhausted after %d attempts (max_attempts=%d): %w", attempt, openAIResponsesIdleMaxAttempts, idleErr)
		}
		delay := time.Duration(attempt) * openAIResponsesRetryBaseDelay
		p.emitOpenAIResponsesRetryDiagnostic(opts, idleErr, attempt, delay, true, false)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return llm.Response{}, ctx.Err()
		case <-timer.C:
		}
	}
}

func (p *openAIResponsesProvider) completeStreamingAttempt(ctx context.Context, params responses.ResponseNewParams, opts llm.CompleteOptions, idleTimeout time.Duration) (llm.Response, error, bool) {
	streamCtx, resetIdle, stopIdle, idleExpired := protocolsupport.NewStreamIdleContext(ctx, idleTimeout)
	defer stopIdle()
	stream := p.client.Responses.NewStreaming(streamCtx, params)
	defer func() { _ = stream.Close() }()

	var items []responses.ResponseOutputItemUnion
	for stream.Next() {
		resetIdle()
		event := stream.Current()
		emitResponsesStreamDelta(opts.OnDelta, event)
		switch event.Type {
		case "error":
			return llm.Response{}, fmt.Errorf("openai responses stream error: %s", firstNonEmpty(event.Message, event.Code, event.RawJSON())), false
		case "response.failed":
			if msg := responseErrorMessage(event.Response); msg != "" {
				return llm.Response{}, fmt.Errorf("openai responses: %s", msg), false
			}
			return llm.Response{}, fmt.Errorf("openai responses failed"), false
		case "response.output_item.done":
			items = append(items, event.Item)
		case "response.done", "response.completed", "response.incomplete":
			finalResp := event.Response
			if len(finalResp.Output) == 0 && len(items) > 0 {
				finalResp.Output = items
			}
			return p.responseFromResponses(&finalResp), nil, false
		}
	}
	if err := stream.Err(); err != nil {
		if idleExpired() {
			return llm.Response{}, err, true
		}
		return llm.Response{}, fmt.Errorf("openai responses stream: %w", err), false
	}
	if len(items) == 0 {
		return llm.Response{}, fmt.Errorf("openai responses stream closed before response.completed"), false
	}
	finalResp := responses.Response{Status: responses.ResponseStatusCompleted, Output: items}
	return p.responseFromResponses(&finalResp), nil, false
}

func (p *openAIResponsesProvider) emitOpenAIResponsesRetryDiagnostic(opts llm.CompleteOptions, err error, attempt int, delay time.Duration, willRetry, exhausted bool) {
	if opts.RetryObserver == nil {
		return
	}
	opts.RetryObserver(llm.ProviderRetryDiagnostic{
		Provider:    p.profile.ID,
		Model:       p.profile.Model,
		Protocol:    p.profile.Protocol,
		Transport:   "sse",
		Operation:   "responses.sse",
		Attempt:     attempt,
		MaxAttempts: openAIResponsesIdleMaxAttempts,
		DelayMS:     delay.Milliseconds(),
		RetryReason: "openai_responses_stream_idle_timeout",
		RawError:    err.Error(),
		WillRetry:   willRetry,
		Exhausted:   exhausted,
	})
}

func emitResponsesStreamDelta(onDelta func(llm.StreamDelta), event responses.ResponseStreamEventUnion) {
	if onDelta == nil {
		return
	}
	switch event.Type {
	case "response.output_text.delta":
		delta := event.AsResponseOutputTextDelta()
		if delta.Delta != "" {
			onDelta(llm.StreamDelta{Kind: "text", Index: int(delta.OutputIndex), Text: delta.Delta})
		}
	case "response.reasoning_summary_text.delta":
		delta := event.AsResponseReasoningSummaryTextDelta()
		if delta.Delta != "" {
			onDelta(llm.StreamDelta{Kind: "reasoning", Index: int(delta.OutputIndex), Text: delta.Delta})
		}
	}
}

func (p *openAIResponsesProvider) responseFromResponses(resp *responses.Response) llm.Response {
	out := llm.Message{Role: llm.RoleAssistant, Model: p.Name()}
	stop := llm.StopEndTurn
	for _, item := range resp.Output {
		switch item.Type {
		case "reasoning":
			var summaries []string
			for _, summary := range item.Summary {
				if summary.Text != "" {
					summaries = append(summaries, summary.Text)
				}
			}
			out.Blocks = append(out.Blocks, llm.Block{
				Type:      llm.BlockReasoning,
				Text:      strings.Join(summaries, "\n"),
				Signature: item.ID,
				Content:   item.EncryptedContent,
				Redacted:  item.EncryptedContent != "",
			})
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "output_text":
					out.Blocks = append(out.Blocks, llm.Block{Type: llm.BlockText, Text: c.Text})
				case "refusal":
					out.Blocks = append(out.Blocks, llm.Block{Type: llm.BlockText, Text: c.Refusal})
				}
			}
		case "function_call":
			stop = llm.StopToolUse
			out.Blocks = append(out.Blocks, llm.Block{
				Type:      llm.BlockToolUse,
				ToolUseID: item.CallID,
				ToolName:  item.Name,
				Input:     llm.ParseToolArguments(item.Arguments),
			})
		}
	}
	if string(resp.Status) == "incomplete" && resp.IncompleteDetails.Reason == "max_output_tokens" {
		stop = llm.StopMaxTokens
	}
	return llm.Response{
		Message:    out,
		StopReason: stop,
		Usage: llm.CanonicalUsage(
			int(resp.Usage.InputTokens),
			int(resp.Usage.OutputTokens),
			int(resp.Usage.InputTokensDetails.CachedTokens),
		),
	}
}

func encodeOpenAIResponseInput(history []llm.Message, profile llm.ProviderProfile) responses.ResponseInputParam {
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
			case llm.BlockText:
				textParts = append(textParts, b.Text)
			case llm.BlockImage:
				if dataURL, ok := providermedia.ImageDataURL(profile.MediaDir, b.Media); ok {
					flushTextToContent()
					imagePart := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
					imagePart.OfInputImage.ImageURL = param.NewOpt(dataURL)
					contentParts = append(contentParts, imagePart)
				} else {
					textParts = append(textParts, llm.UnavailableMediaReferenceText("image", b.Media))
				}
			case llm.BlockToolUse:
				flushMessage()
				out = append(out, responses.ResponseInputItemParamOfFunctionCall(llm.ToolCallArguments(b.ToolName, b.Input), boundedOpenAIResponsesToolCallID(b.ToolUseID), b.ToolName))
			case llm.BlockToolResult:
				flushMessage()
				content := b.Content
				var dataURL string
				var mediaAvailable bool
				if b.Media != nil {
					dataURL, mediaAvailable = providermedia.ImageDataURL(profile.MediaDir, b.Media)
					if mediaAvailable {
						content = llm.ToolResultContentWithMediaReference(b)
					} else {
						content = llm.ToolResultContentWithUnavailableMediaReference(b)
					}
				}
				out = append(out, responses.ResponseInputItemParamOfFunctionCallOutput(boundedOpenAIResponsesToolCallID(b.ToolUseID), content))
				if mediaAvailable {
					imagePart := responses.ResponseInputContentParamOfInputImage(responses.ResponseInputImageDetailAuto)
					imagePart.OfInputImage.ImageURL = param.NewOpt(dataURL)
					out = appendResponseMessage(out, llm.RoleUser, nil, responses.ResponseInputMessageContentListParam{
						responses.ResponseInputContentParamOfInputText(openAIToolResultImageAttribution(b)),
						imagePart,
					})
				}
			case llm.BlockReasoning:
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

func boundedOpenAIResponsesToolCallID(id string) string {
	if len(id) <= openAIResponsesToolCallIDMaxLength {
		return id
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
	return openAIResponsesToolCallIDHashPrefix + digest[:openAIResponsesToolCallIDMaxLength-len(openAIResponsesToolCallIDHashPrefix)]
}

func appendResponseMessage(out responses.ResponseInputParam, role llm.Role, textParts []string, contentParts responses.ResponseInputMessageContentListParam) responses.ResponseInputParam {
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

func toResponseRole(role llm.Role) responses.EasyInputMessageRole {
	switch role {
	case llm.RoleAssistant:
		return responses.EasyInputMessageRoleAssistant
	case llm.RoleSystem:
		return responses.EasyInputMessageRoleSystem
	default:
		return responses.EasyInputMessageRoleUser
	}
}

func toOpenAIResponseTools(tools []llm.ToolSpec) []responses.ToolUnionParam {
	out := make([]responses.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, responses.ToolUnionParam{
			OfFunction: &responses.FunctionToolParam{
				Name:        t.Name,
				Description: param.NewOpt(t.Description),
				Parameters:  llm.NormalizedFunctionParameters(t.Schema),
				Strict:      param.NewOpt(false),
			},
		})
	}
	return out
}
