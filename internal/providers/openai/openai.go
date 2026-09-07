package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/llm"
	providermedia "github.com/juex-ai/juex/internal/providers/internal/media"
	protocolsupport "github.com/juex-ai/juex/internal/providers/internal/protocol"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
	openai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/shared"
)

// openAIProvider wraps the official openai-go client. The same client targets
// every OpenAI-compatible endpoint (DeepSeek, OpenRouter, Ollama, ...) by
// swapping option.WithBaseURL.
type openAIProvider struct {
	profile llm.ProviderProfile
	client  openai.Client
}

func NewOpenAI(profile llm.ProviderProfile, _ any) llm.Provider {
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
	return &openAIProvider{
		profile: profile,
		client:  openai.NewClient(opts...),
	}
}

func (p *openAIProvider) Name() string { return p.profile.ID + ":" + p.profile.Model }

func (p *openAIProvider) Complete(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec) (llm.Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, llm.CompleteOptions{})
}

func (p *openAIProvider) CompleteWithOptions(ctx context.Context, sys string, history []llm.Message, tools []llm.ToolSpec, opts llm.CompleteOptions) (result llm.Response, err error) {
	defer func() { err = protocolsupport.WrapProviderError(err) }()
	providerContext, err := llm.BuildProviderContext(history, p.profile, llm.ProviderContextOptions{})
	if err != nil {
		return llm.Response{}, err
	}
	msgs := make([]openai.ChatCompletionMessageParamUnion, 0, len(providerContext.Messages)+1)
	if sys != "" {
		msgs = append(msgs, openai.SystemMessage(sys))
	}
	msgs = append(msgs, toOpenAIMessages(providerContext.Messages, p.profile)...)

	params := openai.ChatCompletionNewParams{
		Model:    p.profile.Model,
		Messages: msgs,
	}
	if p.profile.Capabilities.Tools {
		params.Tools = toOpenAITools(tools)
	}
	if effort := protocolsupport.RequestThinkingEffort(p.profile, opts); p.profile.Capabilities.ReasoningEffort && effort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(effort)
	}
	if p.profile.Capabilities.MaxOutputTokens && opts.MaxOutputTokens > 0 {
		params.MaxCompletionTokens = openai.Int(int64(opts.MaxOutputTokens))
	}
	if opts.CachePolicy.StablePrefixKey != "" {
		params.PromptCacheKey = openai.String(opts.CachePolicy.StablePrefixKey)
	}
	if p.profile.Capabilities.Streaming {
		return p.completeStreaming(ctx, params, opts)
	}

	completion, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return llm.Response{}, fmt.Errorf("openai: %w", err)
	}
	return p.responseFromChatCompletion(completion, "")
}

func (p *openAIProvider) completeStreaming(ctx context.Context, params openai.ChatCompletionNewParams, opts llm.CompleteOptions) (llm.Response, error) {
	params.StreamOptions = openai.ChatCompletionStreamOptionsParam{IncludeUsage: openai.Bool(true)}
	idleTimeout := protocolsupport.StreamIdleTimeout(opts)
	streamCtx, resetIdle, stopIdle, idleExpired := protocolsupport.NewStreamIdleContext(ctx, idleTimeout)
	defer stopIdle()
	stream := p.client.Chat.Completions.NewStreaming(streamCtx, params)
	defer func() { _ = stream.Close() }()

	acc := openai.ChatCompletionAccumulator{}
	var (
		reasoning   strings.Builder
		streamUsage openai.CompletionUsage
	)
	for stream.Next() {
		resetIdle()
		chunk := stream.Current()
		for _, choice := range chunk.Choices {
			index := int(choice.Index)
			if delta := extractReasoningContent(choice.Delta.RawJSON()); delta != "" {
				reasoning.WriteString(delta)
				if opts.OnDelta != nil {
					opts.OnDelta(llm.StreamDelta{Kind: "reasoning", Index: index, Text: delta})
				}
			}
			if choice.Delta.Content != "" && opts.OnDelta != nil {
				opts.OnDelta(llm.StreamDelta{Kind: "text", Index: index, Text: choice.Delta.Content})
			}
		}
		if !acc.AddChunk(chunk) {
			return llm.Response{}, fmt.Errorf("openai chat stream accumulation failed")
		}
		if chunk.Usage.PromptTokens != 0 || chunk.Usage.CompletionTokens != 0 || chunk.Usage.PromptTokensDetails.CachedTokens != 0 {
			streamUsage = chunk.Usage
		}
	}
	if err := stream.Err(); err != nil {
		if idleExpired() {
			return llm.Response{}, protocolsupport.NewStreamIdleTimeoutError("openai chat stream", idleTimeout, err)
		}
		return llm.Response{}, fmt.Errorf("openai chat stream: %w", err)
	}
	if streamUsage.PromptTokens != 0 || streamUsage.CompletionTokens != 0 || streamUsage.PromptTokensDetails.CachedTokens != 0 {
		acc.Usage = streamUsage
	}
	return p.responseFromChatCompletion(&acc.ChatCompletion, reasoning.String())
}

func (p *openAIProvider) responseFromChatCompletion(completion *openai.ChatCompletion, streamedReasoning string) (llm.Response, error) {
	if len(completion.Choices) == 0 {
		return llm.Response{}, fmt.Errorf("openai: empty choices")
	}
	choice := completion.Choices[0]

	out := llm.Message{Role: llm.RoleAssistant, Model: p.Name()}
	// DeepSeek and similar providers attach `reasoning_content` to the
	// assistant message. We surface it as a Block so it round-trips on the
	// next call (DeepSeek rejects requests that omit it after a thinking
	// turn).
	if rc := firstNonEmpty(streamedReasoning, extractReasoningContent(choice.Message.RawJSON())); rc != "" {
		out.Blocks = append(out.Blocks, llm.Block{Type: llm.BlockReasoning, Text: rc})
	}
	if choice.Message.Content != "" {
		out.Blocks = append(out.Blocks, llm.Block{Type: llm.BlockText, Text: choice.Message.Content})
	}
	for _, tc := range choice.Message.ToolCalls {
		out.Blocks = append(out.Blocks, llm.Block{
			Type:      llm.BlockToolUse,
			ToolUseID: tc.ID,
			ToolName:  tc.Function.Name,
			Input:     llm.ParseToolArguments(tc.Function.Arguments),
		})
	}

	return llm.Response{
		Message:    out,
		StopReason: mapOpenAIStop(string(choice.FinishReason)),
		Usage: llm.CanonicalUsage(
			int(completion.Usage.PromptTokens),
			int(completion.Usage.CompletionTokens),
			int(completion.Usage.PromptTokensDetails.CachedTokens),
		),
	}, nil
}

// toOpenAIMessages converts Juex history into OpenAI-shaped messages,
// splitting user-role tool_result blocks into role=tool messages so the
// tool_call_id <-> tool message linkage is preserved.
func toOpenAIMessages(history []llm.Message, profile llm.ProviderProfile) []openai.ChatCompletionMessageParamUnion {
	var out []openai.ChatCompletionMessageParamUnion
	for _, m := range history {
		switch m.Role {
		case llm.RoleUser:
			var userText strings.Builder
			var userParts []openai.ChatCompletionContentPartUnionParam
			var deferredUserMessages []openai.ChatCompletionMessageParamUnion
			flushUser := func() {
				if userText.Len() == 0 && len(userParts) == 0 {
					return
				}
				if len(userParts) == 0 {
					out = append(out, openai.UserMessage(userText.String()))
					userText.Reset()
					return
				}
				if userText.Len() > 0 {
					userParts = append(userParts, openai.TextContentPart(userText.String()))
					userText.Reset()
				}
				out = append(out, openAIUserContentPartsMessage(userParts))
				userParts = nil
			}
			flushDeferredUserMessages := func() {
				if len(deferredUserMessages) == 0 {
					return
				}
				out = append(out, deferredUserMessages...)
				deferredUserMessages = nil
			}
			for _, b := range m.Blocks {
				switch b.Type {
				case llm.BlockText:
					flushDeferredUserMessages()
					if userText.Len() > 0 {
						userText.WriteString("\n")
					}
					userText.WriteString(b.Text)
				case llm.BlockImage:
					flushDeferredUserMessages()
					if dataURL, ok := providermedia.ImageDataURL(profile.MediaDir, b.Media); ok {
						if userText.Len() > 0 {
							userParts = append(userParts, openai.TextContentPart(userText.String()))
							userText.Reset()
						}
						userParts = append(userParts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
							URL: dataURL,
						}))
						continue
					}
					if userText.Len() > 0 {
						userText.WriteString("\n")
					}
					userText.WriteString(llm.UnavailableMediaReferenceText("image", b.Media))
				case llm.BlockToolResult:
					flushUser()
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
					out = append(out, openai.ToolMessage(content, b.ToolUseID))
					if mediaAvailable {
						deferredUserMessages = append(deferredUserMessages, openAIUserContentPartsMessage([]openai.ChatCompletionContentPartUnionParam{
							openai.TextContentPart(openAIToolResultImageAttribution(b)),
							openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{URL: dataURL}),
						}))
					}
				}
			}
			flushDeferredUserMessages()
			flushUser()
		case llm.RoleAssistant:
			am := openai.ChatCompletionAssistantMessageParam{}
			var textParts []string
			var reasoningParts []string
			var toolCalls []openai.ChatCompletionMessageToolCallParam
			for _, b := range m.Blocks {
				switch b.Type {
				case llm.BlockText:
					textParts = append(textParts, b.Text)
				case llm.BlockReasoning:
					if profile.Capabilities.ReasoningReplay {
						reasoningParts = append(reasoningParts, b.Text)
					}
				case llm.BlockToolUse:
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallParam{
						ID: b.ToolUseID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      b.ToolName,
							Arguments: llm.ToolCallArguments(b.ToolName, b.Input),
						},
					})
				}
			}
			if len(textParts) > 0 {
				am.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: param.NewOpt(strings.Join(textParts, "\n")),
				}
			} else if len(reasoningParts) > 0 && len(toolCalls) == 0 {
				am.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: param.NewOpt(""),
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
		case llm.RoleSystem:
			if text := m.FirstText(); text != "" {
				out = append(out, openai.SystemMessage(text))
			}
		}
	}
	return out
}

func openAIUserContentPartsMessage(parts []openai.ChatCompletionContentPartUnionParam) openai.ChatCompletionMessageParamUnion {
	msg := openai.ChatCompletionUserMessageParam{}
	msg.Content = openai.ChatCompletionUserMessageParamContentUnion{
		OfArrayOfContentParts: parts,
	}
	return openai.ChatCompletionMessageParamUnion{OfUser: &msg}
}

func openAIToolResultImageAttribution(b llm.Block) string {
	if b.ToolName != "" {
		return "Tool result image from " + b.ToolName + " (" + b.ToolUseID + ")."
	}
	return "Tool result image from tool call " + b.ToolUseID + "."
}

func toOpenAITools(tools []llm.ToolSpec) []openai.ChatCompletionToolParam {
	out := make([]openai.ChatCompletionToolParam, 0, len(tools))
	for _, t := range tools {
		out = append(out, openai.ChatCompletionToolParam{
			Function: shared.FunctionDefinitionParam{
				Name:        t.Name,
				Description: param.NewOpt(t.Description),
				Parameters:  shared.FunctionParameters(llm.NormalizedFunctionParameters(t.Schema)),
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

func mapOpenAIStop(s string) llm.StopReason {
	switch s {
	case "stop":
		return llm.StopEndTurn
	case "tool_calls", "function_call":
		return llm.StopToolUse
	case "length":
		return llm.StopMaxTokens
	default:
		return llm.StopOther
	}
}
