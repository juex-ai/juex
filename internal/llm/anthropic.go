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
	cfg     Config
	profile ProviderProfile
	client  anthropic.Client
}

func NewAnthropic(cfg Config, _ any) Provider {
	profile, err := ResolveProfile(cfg)
	if err != nil {
		profile = presetProfile("anthropic")
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
	return &anthropicProvider{
		cfg:     profile.Config(),
		profile: profile,
		client:  anthropic.NewClient(opts...),
	}
}

func (p *anthropicProvider) Name() string { return p.profile.ID + ":" + p.profile.Model }

func (p *anthropicProvider) Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, CompleteOptions{})
}

func (p *anthropicProvider) CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error) {
	if err := validateProviderTranscript(history, p.profile, providerProjectionOptions{}); err != nil {
		return Response{}, err
	}
	maxTokens := int64(4096)
	if p.profile.Capabilities.MaxOutputTokens && opts.MaxOutputTokens > 0 {
		maxTokens = int64(opts.MaxOutputTokens)
	}

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.profile.Model),
		MaxTokens: maxTokens,
		Messages:  toAnthropicMessages(history, p.profile),
	}
	cachePrompt := opts.CachePolicy.StablePrefixKey != ""
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
	stream := p.client.Messages.NewStreaming(ctx, params, option.WithMiddleware(streamDiagnostics.middleware))
	for stream.Next() {
		event := stream.Current()
		if err := msg.Accumulate(event); err != nil {
			return Response{}, anthropicStreamParseErrorFromEvent(p.Name(), event, err)
		}
	}
	if err := stream.Err(); err != nil {
		if streamErr := anthropicStreamParseErrorFromDiagnostics(p.Name(), streamDiagnostics, err); streamErr != nil {
			return Response{}, streamErr
		}
		return Response{}, fmt.Errorf("anthropic: %w", err)
	}
	return p.responseFromMessage(&msg), nil
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

func toAnthropicMessages(history []Message, profile ProviderProfile) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(history))
	for _, m := range projectProviderTranscript(history, profile, providerProjectionOptions{}) {
		var blocks []anthropic.ContentBlockParamUnion
		for _, b := range m.Blocks {
			switch b.Type {
			case BlockText:
				blocks = append(blocks, anthropic.NewTextBlock(b.Text))
			case BlockReasoning:
				if b.Redacted {
					blocks = append(blocks, anthropic.NewRedactedThinkingBlock(b.Content))
				} else {
					blocks = append(blocks, anthropic.NewThinkingBlock(b.Signature, b.Text))
				}
			case BlockToolUse:
				blocks = append(blocks, anthropic.NewToolUseBlock(b.ToolUseID, b.Input, b.ToolName))
			case BlockToolResult:
				blocks = append(blocks, anthropic.NewToolResultBlock(b.ToolUseID, b.Content, b.IsError))
			}
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

func toAnthropicTools(tools []ToolSpec, cachePrompt bool, cacheRetention string) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for i, t := range tools {
		schema := anthropic.ToolInputSchemaParam{
			Properties: normalizedFunctionProperties(t.Schema),
			Required:   normalizedFunctionRequired(t.Schema),
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
