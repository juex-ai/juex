package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/packages/param"
	"github.com/openai/openai-go/responses"
	"github.com/openai/openai-go/shared"
)

const defaultOpenAICodexBaseURL = "https://chatgpt.com/backend-api/codex"

var codexSSERetryBaseDelay = 100 * time.Millisecond

type openAICodexResponsesProvider struct {
	profile   ProviderProfile
	client    openai.Client
	transport string
	ws        *codexResponsesWebsocketTransport
}

func NewOpenAICodexResponses(profile ProviderProfile, client any) Provider {
	profile = cloneProviderProfile(profile)
	if profile.BaseURL == "" {
		profile.BaseURL = defaultOpenAICodexBaseURL
	}
	transport := profile.Compat.CodexTransport
	if transport == "" {
		transport = CodexTransportSSE
		profile.Compat.CodexTransport = transport
	}
	opts := []option.RequestOption{
		option.WithBaseURL(openAICodexResponsesBaseURL(profile.BaseURL)),
		option.WithMaxRetries(providerMaxRetries),
	}
	for k, v := range profile.Headers {
		opts = append(opts, option.WithHeader(k, v))
	}
	opts = append(opts,
		option.WithAPIKey(profile.APIKey),
		option.WithHeader("originator", "juex"),
		option.WithHeader("User-Agent", fmt.Sprintf("juex (%s; %s)", runtime.GOOS, runtime.GOARCH)),
		option.WithHeader("OpenAI-Beta", "responses=experimental"),
		option.WithHeader("Accept", "text/event-stream"),
	)
	if accountID := codexAccountID(profile); accountID != "" {
		opts = append(opts, option.WithHeader("chatgpt-account-id", accountID))
	}
	for k, v := range profile.Query {
		opts = append(opts, option.WithQuery(k, v))
	}
	if httpClient, ok := client.(*http.Client); ok && httpClient != nil {
		opts = append(opts, option.WithHTTPClient(httpClient))
	}
	var httpClient *http.Client
	if c, ok := client.(*http.Client); ok {
		httpClient = c
	}
	return &openAICodexResponsesProvider{
		profile:   profile,
		client:    openai.NewClient(opts...),
		transport: transport,
		ws:        newCodexResponsesWebsocketTransport(profile, httpClient),
	}
}

func (p *openAICodexResponsesProvider) Name() string { return p.profile.ID + ":" + p.profile.Model }

func (p *openAICodexResponsesProvider) Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, CompleteOptions{})
}

func (p *openAICodexResponsesProvider) CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error) {
	providerContext, err := BuildProviderContext(history, p.profile, ProviderContextOptions{OmitReasoning: true})
	if err != nil {
		return Response{}, err
	}
	params := p.codexRequestParams(sys, providerContext.Messages, tools, opts)
	var resp *responses.Response

	switch p.transport {
	case CodexTransportAuto:
		resp, err = p.ws.Complete(ctx, params)
		if err != nil {
			resp, err = p.completeSSE(ctx, params, opts)
		}
	case CodexTransportWebSocket, CodexTransportWebSocketCached:
		resp, err = p.ws.Complete(ctx, params)
	case CodexTransportSSE:
		resp, err = p.completeSSE(ctx, params, opts)
	default:
		return Response{}, fmt.Errorf("openai codex responses: unsupported codex transport %q", p.transport)
	}
	if err != nil {
		return Response{}, fmt.Errorf("openai codex responses: %w", err)
	}
	return p.responseFromCodexResponses(resp), nil
}

func (p *openAICodexResponsesProvider) completeSSE(ctx context.Context, params responses.ResponseNewParams, opts CompleteOptions) (*responses.Response, error) {
	maxAttempts := providerMaxRetries + 1
	idleTimeout := streamIdleTimeout(opts)
	for attempt := 0; ; attempt++ {
		streamCtx, resetIdle, stopIdle, idleExpired := newStreamIdleContext(ctx, idleTimeout)
		stream := p.client.Responses.NewStreaming(streamCtx, params)
		resp, err := readCodexResponsesStream(stream, codexResponsesStreamOptions{
			OnDelta:   opts.OnDelta,
			ResetIdle: resetIdle,
		})
		_ = stream.Close()
		stopIdle()
		if err == nil {
			return resp, nil
		}
		if idleExpired() {
			return nil, fmt.Errorf("codex SSE idle timeout after %s: %w", idleTimeout, err)
		}
		attemptNumber := attempt + 1
		if ctx.Err() != nil || !isRetryableCodexSSEReadError(err) {
			return nil, err
		}
		if attemptNumber >= maxAttempts {
			p.emitCodexSSERetryDiagnostic(opts, err, attemptNumber, maxAttempts, 0, false, true)
			return nil, fmt.Errorf("codex SSE retry exhausted after %d attempts (max_attempts=%d): %w", attemptNumber, maxAttempts, err)
		}
		delay := codexSSERetryDelay(attempt)
		p.emitCodexSSERetryDiagnostic(opts, err, attemptNumber, maxAttempts, delay, true, false)
		if err := waitCodexSSERetry(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (p *openAICodexResponsesProvider) codexRequestParams(sys string, history []Message, tools []ToolSpec, opts CompleteOptions) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(p.profile.Model),
		Store: param.NewOpt(false),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: encodeOpenAIResponseInput(history, p.profile),
		},
		Include:           []responses.ResponseIncludable{responses.ResponseIncludableReasoningEncryptedContent},
		ParallelToolCalls: param.NewOpt(true),
		ToolChoice: responses.ResponseNewParamsToolChoiceUnion{
			OfToolChoiceMode: param.NewOpt(responses.ToolChoiceOptionsAuto),
		},
	}
	if sys != "" {
		params.Instructions = param.NewOpt(sys)
	}
	params.Text.SetExtraFields(map[string]any{"verbosity": "medium"})
	if p.profile.Capabilities.Tools && len(tools) > 0 {
		params.Tools = toOpenAIResponseTools(tools)
	}
	if p.profile.Capabilities.MaxOutputTokens && opts.MaxOutputTokens > 0 {
		params.MaxOutputTokens = param.NewOpt(int64(opts.MaxOutputTokens))
	}
	if opts.CachePolicy.StablePrefixKey != "" {
		params.PromptCacheKey = param.NewOpt(opts.CachePolicy.StablePrefixKey)
	}
	if opts.CachePolicy.Retention != "" {
		params.SetExtraFields(map[string]any{"prompt_cache_retention": opts.CachePolicy.Retention})
	}
	if p.profile.Capabilities.ReasoningEffort && p.profile.ThinkingEffort != "" {
		params.Reasoning = shared.ReasoningParam{
			Effort:  shared.ReasoningEffort(p.profile.ThinkingEffort),
			Summary: shared.ReasoningSummaryAuto,
		}
	}
	return params
}

type codexResponsesStream interface {
	Next() bool
	Current() responses.ResponseStreamEventUnion
	Err() error
}

type codexResponsesStreamOptions struct {
	OnDelta   func(StreamDelta)
	ResetIdle func()
}

func readCodexResponsesStream(stream codexResponsesStream, opts codexResponsesStreamOptions) (*responses.Response, error) {
	var (
		finalResp responses.Response
		hasFinal  bool
		items     []responses.ResponseOutputItemUnion
	)
	for stream.Next() {
		if opts.ResetIdle != nil {
			opts.ResetIdle()
		}
		event := stream.Current()
		switch event.Type {
		case "error":
			return nil, fmt.Errorf("codex error: %s", firstNonEmpty(event.Message, event.Code, event.RawJSON()))
		case "response.failed":
			if msg := responseErrorMessage(event.Response); msg != "" {
				return nil, fmt.Errorf("%s", msg)
			}
			return nil, fmt.Errorf("codex response failed")
		case "response.output_text.delta":
			if opts.OnDelta != nil {
				delta := event.AsResponseOutputTextDelta()
				if delta.Delta != "" {
					opts.OnDelta(StreamDelta{Kind: "text", Index: int(delta.OutputIndex), Text: delta.Delta})
				}
			}
		case "response.output_item.done":
			items = append(items, event.Item)
		case "response.done", "response.completed", "response.incomplete":
			finalResp = event.Response
			hasFinal = true
		}
	}
	if err := stream.Err(); err != nil {
		return nil, &codexSSEReadError{cause: err}
	}
	if !hasFinal {
		if len(items) > 0 {
			return &responses.Response{Status: responses.ResponseStatusCompleted, Output: items}, nil
		}
		return nil, &codexSSEReadError{cause: errors.New("stream closed before response.completed")}
	}
	if len(finalResp.Output) == 0 && len(items) > 0 {
		finalResp.Output = items
	}
	return &finalResp, nil
}

type codexSSEReadError struct {
	cause error
}

func (e *codexSSEReadError) Error() string {
	return fmt.Sprintf("codex SSE read: %v", e.cause)
}

func (e *codexSSEReadError) Unwrap() error {
	return e.cause
}

func isRetryableCodexSSEReadError(err error) bool {
	var readErr *codexSSEReadError
	if !errors.As(err, &readErr) {
		return false
	}
	if errors.Is(readErr.cause, context.Canceled) || errors.Is(readErr.cause, context.DeadlineExceeded) {
		return false
	}
	var apiErr *openai.Error
	return !errors.As(readErr.cause, &apiErr)
}

func codexSSERetryDelay(attempt int) time.Duration {
	return time.Duration(attempt+1) * codexSSERetryBaseDelay
}

func waitCodexSSERetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *openAICodexResponsesProvider) emitCodexSSERetryDiagnostic(opts CompleteOptions, err error, attempt, maxAttempts int, delay time.Duration, willRetry, exhausted bool) {
	if opts.RetryObserver == nil {
		return
	}
	opts.RetryObserver(ProviderRetryDiagnostic{
		Provider:    p.profile.ID,
		Model:       p.profile.Model,
		Protocol:    p.profile.Protocol,
		Transport:   CodexTransportSSE,
		Operation:   "responses.sse",
		Attempt:     attempt,
		MaxAttempts: maxAttempts,
		DelayMS:     delay.Milliseconds(),
		RetryReason: "codex_sse_read",
		RawError:    err.Error(),
		WillRetry:   willRetry,
		Exhausted:   exhausted,
	})
}

func responseErrorMessage(resp responses.Response) string {
	if resp.Error.Message != "" {
		return resp.Error.Message
	}
	if resp.Error.Code != "" {
		return string(resp.Error.Code)
	}
	return ""
}

func (p *openAICodexResponsesProvider) responseFromCodexResponses(resp *responses.Response) Response {
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
	if resp.Status == responses.ResponseStatusIncomplete && resp.IncompleteDetails.Reason == "max_output_tokens" {
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

func openAICodexResponsesBaseURL(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		raw = defaultOpenAICodexBaseURL
	}
	normalized := strings.TrimRight(raw, "/")
	if strings.HasSuffix(normalized, "/responses") {
		normalized = strings.TrimRight(strings.TrimSuffix(normalized, "/responses"), "/")
	}
	if !strings.HasSuffix(normalized, "/codex") {
		normalized += "/codex"
	}
	return normalized
}

func codexAccountID(profile ProviderProfile) string {
	for _, key := range []string{"chatgpt-account-id", "ChatGPT-Account-ID"} {
		if v := strings.TrimSpace(profile.Headers[key]); v != "" {
			return v
		}
	}
	return codexAccountIDFromAccessToken(profile.APIKey)
}

func codexAccountIDFromAccessToken(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	switch v := claims["https://api.openai.com/auth"].(type) {
	case map[string]any:
		if accountID, _ := v["chatgpt_account_id"].(string); strings.TrimSpace(accountID) != "" {
			return strings.TrimSpace(accountID)
		}
	}
	if accountID, _ := claims["chatgpt_account_id"].(string); strings.TrimSpace(accountID) != "" {
		return strings.TrimSpace(accountID)
	}
	return ""
}
