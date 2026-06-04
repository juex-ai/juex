package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultOpenAICodexBaseURL = "https://chatgpt.com/backend-api/codex"
	maxCodexSSELineBytes      = 1 << 20
	maxCodexSSEEventBytes     = 4 << 20
	maxCodexSSEDataLines      = 1024
	maxCodexRetryDelay        = 60 * time.Second
)

type openAICodexResponsesProvider struct {
	cfg     Config
	profile ProviderProfile
	client  *http.Client
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
	httpClient, _ := client.(*http.Client)
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &openAICodexResponsesProvider{
		cfg:     profile.Config(),
		profile: profile,
		client:  httpClient,
	}
}

func (p *openAICodexResponsesProvider) Name() string { return p.profile.ID + ":" + p.profile.Model }

func (p *openAICodexResponsesProvider) Complete(ctx context.Context, sys string, history []Message, tools []ToolSpec) (Response, error) {
	return p.CompleteWithOptions(ctx, sys, history, tools, CompleteOptions{})
}

func (p *openAICodexResponsesProvider) CompleteWithOptions(ctx context.Context, sys string, history []Message, tools []ToolSpec, opts CompleteOptions) (Response, error) {
	body := p.codexRequestBody(sys, history, tools, opts)
	data, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("openai codex responses: encode request: %w", err)
	}
	url := openAICodexResponsesURL(p.profile.BaseURL)
	for attempt := 0; ; attempt++ {
		canRetry := attempt < providerMaxRetries
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return Response{}, fmt.Errorf("openai codex responses: create request: %w", err)
		}
		p.setHeaders(req)
		resp, err := p.client.Do(req)
		if err != nil {
			if ctx.Err() != nil || !canRetry {
				return Response{}, fmt.Errorf("openai codex responses: %w", err)
			}
			if resp != nil && resp.Body != nil {
				resp.Body.Close()
			}
			if err := sleepCodexRetry(ctx, codexRetryDelay(attempt, nil)); err != nil {
				return Response{}, fmt.Errorf("openai codex responses: %w", err)
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			if isCodexRetryableStatus(resp.StatusCode) && canRetry {
				delay := codexRetryDelay(attempt, resp.Header)
				_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
				resp.Body.Close()
				if err := sleepCodexRetry(ctx, delay); err != nil {
					return Response{}, fmt.Errorf("openai codex responses: %w", err)
				}
				continue
			}
			msg := codexHTTPError(resp)
			resp.Body.Close()
			return Response{}, fmt.Errorf("openai codex responses: POST %q: %s", req.URL.String(), msg)
		}
		wire, err := readCodexSSE(resp.Body)
		resp.Body.Close()
		if err != nil {
			return Response{}, fmt.Errorf("openai codex responses: %w", err)
		}
		return p.responseFromCodex(wire), nil
	}
}

func (p *openAICodexResponsesProvider) setHeaders(req *http.Request) {
	for k, v := range p.profile.Headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Authorization", "Bearer "+p.profile.APIKey)
	if accountID := p.codexAccountID(); accountID != "" {
		req.Header.Set("chatgpt-account-id", accountID)
	}
	req.Header.Set("originator", "juex")
	req.Header.Set("User-Agent", fmt.Sprintf("juex (%s; %s)", runtime.GOOS, runtime.GOARCH))
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
}

func (p *openAICodexResponsesProvider) codexAccountID() string {
	for _, key := range []string{"chatgpt-account-id", "ChatGPT-Account-ID"} {
		if v := strings.TrimSpace(p.profile.Headers[key]); v != "" {
			return v
		}
	}
	return codexAccountIDFromAccessToken(p.profile.APIKey)
}

func (p *openAICodexResponsesProvider) codexRequestBody(sys string, history []Message, tools []ToolSpec, opts CompleteOptions) codexResponsesRequest {
	body := codexResponsesRequest{
		Model:             p.profile.Model,
		Store:             false,
		Stream:            true,
		Instructions:      sys,
		Input:             toCodexResponseInput(history, p.profile),
		Include:           []string{"reasoning.encrypted_content"},
		Text:              map[string]string{"verbosity": "medium"},
		ToolChoice:        "auto",
		ParallelToolCalls: true,
	}
	if p.profile.Capabilities.Tools && len(tools) > 0 {
		body.Tools = toCodexResponseTools(tools)
	}
	if p.profile.Capabilities.MaxOutputTokens && opts.MaxOutputTokens > 0 {
		body.MaxOutputTokens = opts.MaxOutputTokens
	}
	if opts.CachePolicy.StablePrefixKey != "" {
		body.PromptCacheKey = opts.CachePolicy.StablePrefixKey
	}
	if opts.CachePolicy.Retention != "" {
		body.PromptCacheRetention = opts.CachePolicy.Retention
	}
	if p.profile.Capabilities.ReasoningEffort && p.profile.ThinkingEffort != "" {
		body.Reasoning = &codexReasoningParam{
			Effort:  p.profile.ThinkingEffort,
			Summary: "auto",
		}
	}
	return body
}

func (p *openAICodexResponsesProvider) responseFromCodex(resp codexWireResponse) Response {
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
			var input map[string]any
			if item.Arguments != "" {
				if err := json.Unmarshal([]byte(item.Arguments), &input); err != nil {
					input = map[string]any{"_raw_arguments": item.Arguments}
				}
			}
			out.Blocks = append(out.Blocks, Block{
				Type:      BlockToolUse,
				ToolUseID: item.CallID,
				ToolName:  item.Name,
				Input:     input,
			})
		}
	}
	if resp.Status == "incomplete" && resp.IncompleteDetails.Reason == "max_output_tokens" {
		stop = StopMaxTokens
	}
	return Response{
		Message:    out,
		StopReason: stop,
		Usage: Usage{
			InputTokens:       resp.Usage.InputTokens,
			OutputTokens:      resp.Usage.OutputTokens,
			CachedInputTokens: resp.Usage.InputTokensDetails.CachedTokens,
		},
	}
}

func openAICodexResponsesURL(baseURL string) string {
	raw := strings.TrimSpace(baseURL)
	if raw == "" {
		raw = defaultOpenAICodexBaseURL
	}
	normalized := strings.TrimRight(raw, "/")
	switch {
	case strings.HasSuffix(normalized, "/codex/responses"):
		return normalized
	case strings.HasSuffix(normalized, "/codex"):
		return normalized + "/responses"
	default:
		return normalized + "/codex/responses"
	}
}

func isCodexRetryableStatus(status int) bool {
	switch status {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusConflict, http.StatusTooManyRequests:
		return true
	default:
		return status >= 500
	}
}

func codexRetryDelay(attempt int, h http.Header) time.Duration {
	if h != nil {
		if raw := strings.TrimSpace(h.Get("retry-after-ms")); raw != "" {
			if ms, err := strconv.Atoi(raw); err == nil && ms >= 0 {
				return minCodexRetryDelay(time.Duration(ms) * time.Millisecond)
			}
		}
		if raw := strings.TrimSpace(h.Get("Retry-After")); raw != "" {
			if secs, err := strconv.Atoi(raw); err == nil && secs >= 0 {
				return minCodexRetryDelay(time.Duration(secs) * time.Second)
			}
			if when, err := http.ParseTime(raw); err == nil {
				delay := time.Until(when)
				if delay > 0 {
					return minCodexRetryDelay(delay)
				}
				return 0
			}
		}
	}
	if attempt < 0 {
		attempt = 0
	}
	if attempt > 8 {
		return withCodexRetryJitter(maxCodexRetryDelay)
	}
	delay := time.Duration(1<<attempt) * 200 * time.Millisecond
	return withCodexRetryJitter(minCodexRetryDelay(delay))
}

func minCodexRetryDelay(delay time.Duration) time.Duration {
	if delay > maxCodexRetryDelay {
		return maxCodexRetryDelay
	}
	return delay
}

func withCodexRetryJitter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}
	jitterRange := delay / 5
	if jitterRange <= 0 {
		return delay
	}
	jitter := time.Duration(rand.Int63n(int64(jitterRange)))
	return minCodexRetryDelay(delay + jitter)
}

func sleepCodexRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
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

type codexResponsesRequest struct {
	Model                string               `json:"model"`
	Store                bool                 `json:"store"`
	Stream               bool                 `json:"stream"`
	Instructions         string               `json:"instructions,omitempty"`
	Input                []any                `json:"input"`
	Include              []string             `json:"include,omitempty"`
	Tools                []codexResponseTool  `json:"tools,omitempty"`
	ToolChoice           string               `json:"tool_choice,omitempty"`
	ParallelToolCalls    bool                 `json:"parallel_tool_calls"`
	Reasoning            *codexReasoningParam `json:"reasoning,omitempty"`
	Text                 map[string]string    `json:"text,omitempty"`
	MaxOutputTokens      int                  `json:"max_output_tokens,omitempty"`
	PromptCacheKey       string               `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention string               `json:"prompt_cache_retention,omitempty"`
}

type codexReasoningParam struct {
	Effort  string `json:"effort"`
	Summary string `json:"summary,omitempty"`
}

type codexResponseTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      bool           `json:"strict"`
}

func toCodexResponseInput(history []Message, profile ProviderProfile) []any {
	var out []any
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
				out = appendCodexTextMessage(out, m.Role, textParts)
				textParts = nil
				out = append(out, map[string]any{
					"type":      "function_call",
					"call_id":   b.ToolUseID,
					"name":      b.ToolName,
					"arguments": toolCallArguments(b.Input),
				})
			case BlockToolResult:
				if !profile.Capabilities.Tools {
					continue
				}
				out = appendCodexTextMessage(out, m.Role, textParts)
				textParts = nil
				out = append(out, map[string]any{
					"type":    "function_call_output",
					"call_id": b.ToolUseID,
					"output":  b.Content,
				})
			case BlockReasoning:
				if !profile.Capabilities.ReasoningReplay || b.Signature == "" {
					continue
				}
				out = appendCodexTextMessage(out, m.Role, textParts)
				textParts = nil
				reasoning := map[string]any{
					"type":    "reasoning",
					"id":      b.Signature,
					"summary": []map[string]string{},
				}
				if b.Text != "" {
					reasoning["summary"] = []map[string]string{{"type": "summary_text", "text": b.Text}}
				}
				if b.Content != "" {
					reasoning["encrypted_content"] = b.Content
				}
				out = append(out, reasoning)
			}
		}
		out = appendCodexTextMessage(out, m.Role, textParts)
	}
	return out
}

func appendCodexTextMessage(out []any, role Role, parts []string) []any {
	if len(parts) == 0 {
		return out
	}
	text := strings.Join(parts, "\n")
	if role == RoleAssistant {
		return append(out, map[string]any{
			"type":   "message",
			"role":   "assistant",
			"status": "completed",
			"content": []map[string]any{{
				"type":        "output_text",
				"text":        text,
				"annotations": []any{},
			}},
		})
	}
	return append(out, map[string]any{
		"role": codexInputRole(role),
		"content": []map[string]string{{
			"type": "input_text",
			"text": text,
		}},
	})
}

func codexInputRole(role Role) string {
	switch role {
	case RoleSystem:
		return "system"
	default:
		return "user"
	}
}

func toCodexResponseTools(tools []ToolSpec) []codexResponseTool {
	out := make([]codexResponseTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, codexResponseTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  normalizedFunctionParameters(t.Schema),
			Strict:      false,
		})
	}
	return out
}

type codexSSEEvent struct {
	Type     string             `json:"type"`
	Code     string             `json:"code,omitempty"`
	Message  string             `json:"message,omitempty"`
	Response *codexWireResponse `json:"response,omitempty"`
	Item     *codexWireItem     `json:"item,omitempty"`
}

type codexWireResponse struct {
	ID                string          `json:"id,omitempty"`
	Model             string          `json:"model,omitempty"`
	Status            string          `json:"status,omitempty"`
	Output            []codexWireItem `json:"output,omitempty"`
	Usage             codexWireUsage  `json:"usage,omitempty"`
	IncompleteDetails codexIncomplete `json:"incomplete_details,omitempty"`
	Error             *codexWireError `json:"error,omitempty"`
}

type codexWireItem struct {
	Type             string             `json:"type"`
	ID               string             `json:"id,omitempty"`
	Role             string             `json:"role,omitempty"`
	Status           string             `json:"status,omitempty"`
	Summary          []codexWireSummary `json:"summary,omitempty"`
	EncryptedContent string             `json:"encrypted_content,omitempty"`
	Content          []codexWireContent `json:"content,omitempty"`
	CallID           string             `json:"call_id,omitempty"`
	Name             string             `json:"name,omitempty"`
	Arguments        string             `json:"arguments,omitempty"`
}

type codexWireSummary struct {
	Type string `json:"type,omitempty"`
	Text string `json:"text,omitempty"`
}

type codexWireContent struct {
	Type    string `json:"type,omitempty"`
	Text    string `json:"text,omitempty"`
	Refusal string `json:"refusal,omitempty"`
}

type codexWireUsage struct {
	InputTokens        int                   `json:"input_tokens,omitempty"`
	OutputTokens       int                   `json:"output_tokens,omitempty"`
	TotalTokens        int                   `json:"total_tokens,omitempty"`
	InputTokensDetails codexWireTokenDetails `json:"input_tokens_details,omitempty"`
}

type codexWireTokenDetails struct {
	CachedTokens int `json:"cached_tokens,omitempty"`
}

type codexIncomplete struct {
	Reason string `json:"reason,omitempty"`
}

type codexWireError struct {
	Message string `json:"message,omitempty"`
	Code    string `json:"code,omitempty"`
	Type    string `json:"type,omitempty"`
}

func readCodexSSE(r io.Reader) (codexWireResponse, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxCodexSSELineBytes)
	var (
		dataLines []string
		dataBytes int
		finalResp *codexWireResponse
		items     []codexWireItem
	)
	process := func() error {
		if len(dataLines) == 0 {
			dataBytes = 0
			return nil
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = nil
		dataBytes = 0
		if data == "" || data == "[DONE]" {
			return nil
		}
		var ev codexSSEEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return err
		}
		switch ev.Type {
		case "error":
			return fmt.Errorf("codex error: %s", firstNonEmpty(ev.Message, ev.Code, data))
		case "response.failed":
			if ev.Response != nil && ev.Response.Error != nil {
				return fmt.Errorf("%s", firstNonEmpty(ev.Response.Error.Message, ev.Response.Error.Code, ev.Response.Error.Type))
			}
			return fmt.Errorf("codex response failed")
		case "response.output_item.done":
			if ev.Item != nil {
				items = append(items, *ev.Item)
			}
		case "response.done", "response.completed", "response.incomplete":
			if ev.Response != nil {
				cp := *ev.Response
				finalResp = &cp
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			if err := process(); err != nil {
				return codexWireResponse{}, err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if len(dataLines) >= maxCodexSSEDataLines {
				return codexWireResponse{}, fmt.Errorf("codex SSE event has too many data lines")
			}
			dataBytes += len(data)
			if dataBytes > maxCodexSSEEventBytes {
				return codexWireResponse{}, fmt.Errorf("codex SSE event data exceeds %d bytes", maxCodexSSEEventBytes)
			}
			dataLines = append(dataLines, data)
		}
	}
	if err := scanner.Err(); err != nil {
		return codexWireResponse{}, fmt.Errorf("codex SSE read: %w", err)
	}
	if err := process(); err != nil {
		return codexWireResponse{}, err
	}
	if finalResp == nil {
		if len(items) > 0 {
			return codexWireResponse{Status: "completed", Output: items}, nil
		}
		return codexWireResponse{}, fmt.Errorf("stream closed before response.completed")
	}
	if len(finalResp.Output) == 0 && len(items) > 0 {
		finalResp.Output = items
	}
	return *finalResp, nil
}

func codexHTTPError(resp *http.Response) string {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	body := strings.TrimSpace(string(data))
	var parsed struct {
		Error *codexWireError `json:"error"`
	}
	if err := json.Unmarshal(data, &parsed); err == nil && parsed.Error != nil {
		body = firstNonEmpty(parsed.Error.Message, parsed.Error.Code, parsed.Error.Type, body)
	}
	if body == "" {
		body = resp.Status
	}
	return fmt.Sprintf("%s; body=%s", resp.Status, body)
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
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	accountID, _ := auth["chatgpt_account_id"].(string)
	return accountID
}
