package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

// anthropicProvider wraps the official anthropic-sdk-go client and translates
// between Juex's canonical Message form and the SDK's request/response types.
//
// We deliberately keep the SDK confined to this file — every other layer
// works against the canonical types in types.go, so swapping SDK versions or
// dropping back to raw HTTP only touches this file.
type anthropicProvider struct {
	profile ProviderProfile
	client  anthropic.Client
}

func NewAnthropic(profile ProviderProfile, _ any) Provider {
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
	return &anthropicProvider{
		profile: profile,
		client:  anthropic.NewClient(opts...),
	}
}

func (p *anthropicProvider) Name() string { return p.profile.ID + ":" + p.profile.Model }

func (p *anthropicProvider) Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, CompleteOptions{})
}

func (p *anthropicProvider) CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error) {
	providerContext, err := BuildProviderContext(history, p.profile, ProviderContextOptions{})
	if err != nil {
		return Response{}, err
	}
	maxTokens := int64(4096)
	if p.profile.Capabilities.MaxOutputTokens && opts.MaxOutputTokens > 0 {
		maxTokens = int64(opts.MaxOutputTokens)
	}

	cachePrompt := opts.CachePolicy.StablePrefixKey != ""
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.profile.Model),
		MaxTokens: maxTokens,
		Messages:  toAnthropicMessages(providerContext.Messages, p.profile, cachePrompt, opts.CachePolicy.Retention),
	}
	if p.profile.Capabilities.Tools {
		params.Tools = toAnthropicTools(tools, cachePrompt, opts.CachePolicy.Retention)
	}
	if p.profile.Capabilities.ReasoningEffort {
		if p.profile.ThinkingEffort != "" {
			params.OutputConfig = anthropic.OutputConfigParam{
				Effort: anthropic.OutputConfigEffort(p.profile.ThinkingEffort),
			}
		}
		params.Thinking = anthropic.ThinkingConfigParamUnion{
			OfAdaptive: &anthropic.ThinkingConfigAdaptiveParam{
				Display: anthropic.ThinkingConfigAdaptiveDisplaySummarized,
			},
		}
	}
	if sys != "" {
		systemBlock := anthropic.TextBlockParam{Text: sys}
		if cachePrompt {
			systemBlock.CacheControl = anthropicCacheControl(opts.CachePolicy.Retention)
		}
		params.System = []anthropic.TextBlockParam{systemBlock}
	}

	if !p.profile.Capabilities.Streaming {
		msg, err := p.client.Messages.New(ctx, params)
		if err != nil {
			return Response{}, fmt.Errorf("anthropic: %w", err)
		}
		return p.responseFromMessage(msg), nil
	}

	msg := anthropic.Message{}
	streamDiagnostics := &anthropicStreamDiagnostics{}
	idleTimeout := streamIdleTimeout(opts)
	streamCtx, resetIdle, stopIdle, idleExpired := newStreamIdleContext(ctx, idleTimeout)
	defer stopIdle()
	stream := p.client.Messages.NewStreaming(streamCtx, params, option.WithMiddleware(streamDiagnostics.middleware))
	for stream.Next() {
		resetIdle()
		event := stream.Current()
		emitAnthropicStreamDelta(opts.OnDelta, event)
		if err := msg.Accumulate(event); err != nil {
			return Response{}, anthropicStreamParseErrorFromEvent(p.Name(), event, err)
		}
	}
	if err := stream.Err(); err != nil {
		if idleExpired() {
			return Response{}, fmt.Errorf("anthropic stream idle timeout after %s: %w", idleTimeout, err)
		}
		if streamErr := anthropicStreamParseErrorFromDiagnostics(p.Name(), streamDiagnostics, err); streamErr != nil {
			return Response{}, streamErr
		}
		return Response{}, fmt.Errorf("anthropic: %w", err)
	}
	return p.responseFromMessage(&msg), nil
}

func emitAnthropicStreamDelta(onDelta func(StreamDelta), event anthropic.MessageStreamEventUnion) {
	if onDelta == nil || event.Type != "content_block_delta" {
		return
	}
	switch event.Delta.Type {
	case "thinking_delta":
		if event.Delta.Thinking != "" {
			onDelta(StreamDelta{Kind: "reasoning", Index: int(event.Index), Text: event.Delta.Thinking})
		}
	case "text_delta":
		if event.Delta.Text != "" {
			onDelta(StreamDelta{Kind: "text", Index: int(event.Index), Text: event.Delta.Text})
		}
	}
}

func anthropicStreamParseErrorFromEvent(provider string, event anthropic.MessageStreamEventUnion, cause error) *StreamParseError {
	raw := trimStreamPreview(event.RawJSON())
	eventType := event.Type
	if eventType == "" {
		eventType = extractAnthropicStreamType(raw)
	}
	idx, hasIndex := extractAnthropicStreamIndex(raw)
	if hasAnthropicContentBlockIndex(eventType) {
		idx = event.Index
		hasIndex = true
	}
	return newAnthropicStreamParseError(provider, anthropicStreamDiagnostic{
		EventType:  eventType,
		Index:      idx,
		HasIndex:   hasIndex,
		RawPreview: raw,
	}, cause)
}

func anthropicStreamParseErrorFromDiagnostics(provider string, diagnostics *anthropicStreamDiagnostics, cause error) *StreamParseError {
	diag := diagnostics.last()
	if !isAnthropicParsedStreamEvent(diag.EventType) {
		return nil
	}
	return newAnthropicStreamParseError(provider, diag, cause)
}

func newAnthropicStreamParseError(provider string, diag anthropicStreamDiagnostic, cause error) *StreamParseError {
	eventType := diag.EventType
	if eventType == "" {
		eventType = "stream"
	}
	return &StreamParseError{
		Kind:       StreamParseErrorKindAnthropic,
		Provider:   provider,
		EventType:  eventType,
		Index:      diag.Index,
		HasIndex:   diag.HasIndex,
		RawPreview: diag.RawPreview,
		Cause:      cause,
	}
}

func hasAnthropicContentBlockIndex(eventType string) bool {
	switch eventType {
	case "content_block_start", "content_block_delta", "content_block_stop":
		return true
	default:
		return false
	}
}

func isAnthropicParsedStreamEvent(eventType string) bool {
	switch eventType {
	case "message_start", "message_delta", "message_stop", "content_block_start", "content_block_delta", "content_block_stop":
		return true
	default:
		return false
	}
}

func (p *anthropicProvider) responseFromMessage(msg *anthropic.Message) Response {
	out := Message{Role: RoleAssistant, Model: p.Name()}
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			out.Blocks = append(out.Blocks, Block{Type: BlockText, Text: block.Text})
		case "thinking":
			out.Blocks = append(out.Blocks, Block{
				Type:      BlockReasoning,
				Text:      block.Thinking,
				Signature: block.Signature,
			})
		case "redacted_thinking":
			out.Blocks = append(out.Blocks, Block{
				Type:     BlockReasoning,
				Content:  block.Data,
				Redacted: true,
			})
		case "tool_use":
			var input map[string]any
			if len(block.Input) > 0 {
				if err := json.Unmarshal(block.Input, &input); err != nil {
					input = map[string]any{"_raw_input": string(block.Input)}
				}
			}
			out.Blocks = append(out.Blocks, Block{
				Type:      BlockToolUse,
				ToolUseID: block.ID,
				ToolName:  block.Name,
				Input:     input,
			})
		}
	}

	return Response{
		Message:    out,
		StopReason: mapAnthropicStop(string(msg.StopReason)),
		Usage: Usage{
			InputTokens:       int(msg.Usage.InputTokens),
			OutputTokens:      int(msg.Usage.OutputTokens),
			CachedInputTokens: int(msg.Usage.CacheReadInputTokens),
		},
	}
}

func toAnthropicMessages(history []Message, profile ProviderProfile, cachePrompt bool, cacheRetention string) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(history))
	cacheMsg, cacheBlock := anthropicHistoryCacheBreakpoint(history, cachePrompt)
	for msgIndex, m := range history {
		var blocks []anthropic.ContentBlockParamUnion
		for blockIndex, b := range m.Blocks {
			var block anthropic.ContentBlockParamUnion
			switch b.Type {
			case BlockText:
				block = anthropic.NewTextBlock(b.Text)
			case BlockImage:
				if imageBlock, ok := anthropicImageBlock(profile.WorkDir, b.Media); ok {
					block = imageBlock
				} else {
					block = anthropic.NewTextBlock(mediaReferenceText("image", b.Media))
				}
			case BlockReasoning:
				if b.Redacted {
					block = anthropic.NewRedactedThinkingBlock(b.Content)
				} else {
					block = anthropic.NewThinkingBlock(b.Signature, b.Text)
				}
			case BlockToolUse:
				block = anthropic.NewToolUseBlock(b.ToolUseID, b.Input, b.ToolName)
			case BlockToolResult:
				block = anthropicToolResultBlock(profile.WorkDir, b)
			default:
				continue
			}
			if msgIndex == cacheMsg && blockIndex == cacheBlock {
				setAnthropicBlockCacheControl(&block, cacheRetention)
			}
			blocks = append(blocks, block)
		}
		switch m.Role {
		case RoleUser:
			out = append(out, anthropic.NewUserMessage(blocks...))
		case RoleAssistant:
			out = append(out, anthropic.NewAssistantMessage(blocks...))
		}
	}
	return out
}

func anthropicImageBlock(workDir string, media *MediaRef) (anthropic.ContentBlockParamUnion, bool) {
	encoded, mediaType, ok := readImageBase64(workDir, media)
	if !ok {
		return anthropic.ContentBlockParamUnion{}, false
	}
	return anthropic.NewImageBlockBase64(mediaType, encoded), true
}

func anthropicToolResultBlock(workDir string, b Block) anthropic.ContentBlockParamUnion {
	if b.Media == nil {
		return anthropic.NewToolResultBlock(b.ToolUseID, b.Content, b.IsError)
	}
	content := make([]anthropic.ToolResultBlockParamContentUnion, 0, 2)
	if b.Content != "" {
		content = append(content, anthropic.ToolResultBlockParamContentUnion{
			OfText: &anthropic.TextBlockParam{Text: b.Content},
		})
	}
	if encoded, mediaType, ok := readImageBase64(workDir, b.Media); ok {
		content = append(content, anthropic.ToolResultBlockParamContentUnion{
			OfImage: &anthropic.ImageBlockParam{
				Source: anthropic.ImageBlockParamSourceUnion{
					OfBase64: &anthropic.Base64ImageSourceParam{
						Data:      encoded,
						MediaType: anthropic.Base64ImageSourceMediaType(mediaType),
					},
				},
			},
		})
	} else {
		content = append(content, anthropic.ToolResultBlockParamContentUnion{
			OfText: &anthropic.TextBlockParam{Text: mediaReferenceText("tool_result_image", b.Media)},
		})
	}
	return anthropic.ContentBlockParamUnion{
		OfToolResult: &anthropic.ToolResultBlockParam{
			ToolUseID: b.ToolUseID,
			Content:   content,
			IsError:   param.NewOpt(b.IsError),
		},
	}
}

func anthropicHistoryCacheBreakpoint(history []Message, enabled bool) (int, int) {
	if !enabled {
		return -1, -1
	}
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Kind == MessageKindRuntimeContext {
			continue
		}
		for j := len(history[i].Blocks) - 1; j >= 0; j-- {
			if anthropicCacheableHistoryBlock(history[i].Blocks[j]) {
				return i, j
			}
		}
	}
	return -1, -1
}

func anthropicCacheableHistoryBlock(block Block) bool {
	switch block.Type {
	case BlockText, BlockImage, BlockToolUse, BlockToolResult:
		return true
	default:
		return false
	}
}

func setAnthropicBlockCacheControl(block *anthropic.ContentBlockParamUnion, retention string) {
	cc := anthropicCacheControl(retention)
	switch {
	case block.OfText != nil:
		block.OfText.CacheControl = cc
	case block.OfImage != nil:
		block.OfImage.CacheControl = cc
	case block.OfToolUse != nil:
		block.OfToolUse.CacheControl = cc
	case block.OfToolResult != nil:
		block.OfToolResult.CacheControl = cc
	}
}

func toAnthropicTools(tools []ToolSpec, cachePrompt bool, cacheRetention string) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for i, t := range tools {
		normalized := normalizedFunctionParameters(t.Schema)
		schema := anthropic.ToolInputSchemaParam{
			Properties: normalized["properties"],
			Required:   normalizedFunctionRequired(t.Schema),
		}
		if additionalProperties, ok := normalized["additionalProperties"]; ok && additionalProperties != nil {
			schema.ExtraFields = map[string]any{"additionalProperties": additionalProperties}
		}
		tool := anthropic.ToolParam{
			Name:        t.Name,
			InputSchema: schema,
			Description: param.NewOpt(t.Description),
		}
		if cachePrompt && i == len(tools)-1 {
			tool.CacheControl = anthropicCacheControl(cacheRetention)
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return out
}

func anthropicCacheControl(retention string) anthropic.CacheControlEphemeralParam {
	cc := anthropic.NewCacheControlEphemeralParam()
	switch retention {
	case "1h", "24h":
		cc.TTL = anthropic.CacheControlEphemeralTTLTTL1h
	case "5m":
		cc.TTL = anthropic.CacheControlEphemeralTTLTTL5m
	}
	return cc
}

func mapAnthropicStop(s string) StopReason {
	switch s {
	case "end_turn", "stop_sequence":
		return StopEndTurn
	case "tool_use":
		return StopToolUse
	case "max_tokens":
		return StopMaxTokens
	default:
		return StopOther
	}
}
