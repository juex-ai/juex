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
	cfg    Config
	client anthropic.Client
}

func NewAnthropic(cfg Config, _ any) Provider {
	opts := []option.RequestOption{option.WithAPIKey(cfg.APIKey)}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	return &anthropicProvider{
		cfg:    cfg,
		client: anthropic.NewClient(opts...),
	}
}

func (p *anthropicProvider) Name() string { return "anthropic:" + p.cfg.Model }

func (p *anthropicProvider) Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(p.cfg.Model),
		MaxTokens: 4096,
		Messages:  toAnthropicMessages(history),
		Tools:     toAnthropicTools(tools),
	}
	if sys != "" {
		params.System = []anthropic.TextBlockParam{{Text: sys}}
	}

	msg, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return Response{}, fmt.Errorf("anthropic: %w", err)
	}

	out := Message{Role: RoleAssistant}
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
			InputTokens:  int(msg.Usage.InputTokens),
			OutputTokens: int(msg.Usage.OutputTokens),
		},
	}, nil
}

func toAnthropicMessages(history []Message) []anthropic.MessageParam {
	out := make([]anthropic.MessageParam, 0, len(history))
	for _, m := range history {
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
		if len(blocks) == 0 {
			continue
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

func toAnthropicTools(tools []ToolSpec) []anthropic.ToolUnionParam {
	out := make([]anthropic.ToolUnionParam, 0, len(tools))
	for _, t := range tools {
		schema := anthropic.ToolInputSchemaParam{}
		if props, ok := t.Schema["properties"]; ok {
			schema.Properties = props
		}
		if req, ok := t.Schema["required"].([]string); ok {
			schema.Required = req
		} else if reqAny, ok := t.Schema["required"].([]any); ok {
			for _, r := range reqAny {
				if s, ok := r.(string); ok {
					schema.Required = append(schema.Required, s)
				}
			}
		}
		tool := anthropic.ToolParam{
			Name:        t.Name,
			InputSchema: schema,
			Description: param.NewOpt(t.Description),
		}
		out = append(out, anthropic.ToolUnionParam{OfTool: &tool})
	}
	return out
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
