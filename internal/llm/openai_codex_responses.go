package llm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
const codexSSERetryBaseDelay = 100 * time.Millisecond

type openAICodexResponsesProvider struct {
	cfg       Config
	profile   ProviderProfile
	client    openai.Client
	transport string
	ws        *codexResponsesWebsocketTransport
}

func NewOpenAICodexResponses(cfg Config, client any) Provider {
	profile, err := ResolveProfile(cfg)
	if err != nil {
		profile = presetProfile("openai-codex")
		if cfg.ID != "" {
			profile.ID = cfg.ID
		}
		profile.APIKey = cfg.APIKey
		profile.Model = cfg.Model
		profile.BaseURL = cfg.BaseURL
		profile.ThinkingEffort = cfg.ThinkingEffort
		profile.Headers = cloneStringMap(cfg.Headers)
		profile.Query = cloneStringMap(cfg.Query)
	}
	if profile.BaseURL == "" {
		profile.BaseURL = defaultOpenAICodexBaseURL
	}
	transport, err := NormalizeCodexTransport(profile.Compat.CodexTransport)
	if err != nil {
		transport = CodexTransportSSE
	}
	if transport == "" {
		transport = CodexTransportSSE
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
		cfg:       profile.Config(),
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
	if err := validateProviderTranscript(history, p.profile, providerProjectionOptions{OmitReasoning: true}); err != nil {
		return Response{}, err
	}
	params := p.codexRequestParams(sys, history, tools, opts)
	var resp *responses.Response
	var err error

	switch p.transport {
	case CodexTransportAuto:
		resp, err = p.ws.Complete(ctx, params)
		if err != nil {
			resp, err = p.completeSSE(ctx, params)
		}
	case CodexTransportWebSocket, CodexTransportWebSocketCached:
		resp, err = p.ws.Complete(ctx, params)
	default:
		resp, err = p.completeSSE(ctx, params)
	}
	if err != nil {
		return Response{}, fmt.Errorf("openai codex responses: %w", err)
	}
	return p.responseFromCodexResponses(resp), nil
}

func (p *openAICodexResponsesProvider) completeSSE(ctx context.Context, params responses.ResponseNewParams) (*responses.Response, error) {
	for attempt := 0; attempt <= providerMaxRetries; attempt++ {
		stream := p.client.Responses.NewStreaming(ctx, params)
		resp, err := readCodexResponsesStream(stream)
		_ = stream.Close()
		if err == nil {
			return resp, nil
		}
		if attempt == providerMaxRetries || !isRetryableCodexSSEReadError(err) || ctx.Err() != nil {
			return nil, err
		}
		if err := waitCodexSSERetry(ctx, attempt); err != nil {
			return nil, err
		}
	}
	return nil, fmt.Errorf("codex SSE retry exhausted")
}

func (p *openAICodexResponsesProvider) codexRequestParams(sys string, history []Message, tools []ToolSpec, opts CompleteOptions) responses.ResponseNewParams {
	params := responses.ResponseNewParams{
		Model: shared.ResponsesModel(p.profile.Model),
		Store: param.NewOpt(false),
		Input: responses.ResponseNewParamsInputUnion{
			OfInputItemList: toOpenAIResponseInputWithOptions(history, p.profile, responseInputOptions{
				OmitReasoning: true,
			}),
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

func readCodexResponsesStream(stream codexResponsesStream) (*responses.Response, error) {
	var (
		finalResp responses.Response
		hasFinal  bool
		items     []responses.ResponseOutputItemUnion
	)
	for stream.Next() {
		event := stream.Current()
		switch event.Type {
		case "error":
			return nil, fmt.Errorf("codex error: %s", firstNonEmpty(event.Message, event.Code, event.RawJSON()))
		case "response.failed":
			if msg := responseErrorMessage(event.Response); msg != "" {
				return nil, fmt.Errorf("%s", msg)
			}
			return nil, fmt.Errorf("codex response failed")
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
	if errors.Is(readErr.cause, io.EOF) || errors.Is(readErr.cause, io.ErrUnexpectedEOF) {
		return true
	}
	msg := strings.ToLower(readErr.cause.Error())
	for _, needle := range []string{
		"eof",
		"connection reset",
		"broken pipe",
		"server closed idle connection",
		"use of closed network connection",
		"http2: client connection lost",
		"stream reset",
		"stream closed",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func waitCodexSSERetry(ctx context.Context, attempt int) error {
	delay := time.Duration(attempt+1) * codexSSERetryBaseDelay
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
