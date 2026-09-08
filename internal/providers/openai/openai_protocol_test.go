package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/juex-ai/juex/internal/foundation/artifact"
	"github.com/juex-ai/juex/internal/foundation/llm"
	protocolsupport "github.com/juex-ai/juex/internal/providers/internal/protocol"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
)

func TestOpenAI_RoundTrip(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("missing auth: %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"cmpl_1","object":"chat.completion","model":"gpt-test",
			"choices":[{
				"index":0,
				"message":{
					"role":"assistant",
					"content":"hello back",
					"tool_calls":[{"id":"call_1","type":"function","function":{"name":"grep","arguments":"{\"pattern\":\"foo\"}"}}]
				},
				"finish_reason":"tool_calls"
			}],
			"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}
		}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{
		Protocol: string(llm.ProtocolOpenAIChat),
		BaseURL:  srv.URL,
		APIKey:   "test-key",
		Model:    "gpt-test",
	}), nil)

	resp, err := p.Complete(context.Background(), "system text",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "hello")},
		[]llm.ToolSpec{{Name: "grep", Description: "grep", Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"pattern": map[string]any{"type": "string"}},
			"required":   []string{"pattern"},
		}}},
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.StopReason != llm.StopToolUse {
		t.Errorf("stop reason = %s", resp.StopReason)
	}
	if resp.Message.FirstText() != "hello back" {
		t.Errorf("text = %q", resp.Message.FirstText())
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ToolName != "grep" {
		t.Errorf("tool calls = %+v", calls)
	}
	if calls[0].Input["pattern"] != "foo" {
		t.Errorf("tool input = %+v", calls[0].Input)
	}
}

func TestOpenAI_StreamsReasoningTextAndUsage(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"id":"cmpl_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"plan "},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"cmpl_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"hel"},"finish_reason":null}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"cmpl_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"content":"lo","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"x\"}"}}]},"finish_reason":"tool_calls"}]}`+"\n\n")
		fmt.Fprint(w, `data: {"id":"cmpl_1","object":"chat.completion.chunk","created":1,"model":"m","choices":[],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12,"prompt_tokens_details":{"cached_tokens":4}}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestProfile(t, providerprofile.Config{Protocol: string(llm.ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
	var deltas []llm.StreamDelta
	resp, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		OnDelta: func(delta llm.StreamDelta) { deltas = append(deltas, delta) },
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	streamOptions, _ := capturedBody["stream_options"].(map[string]any)
	if capturedBody["stream"] != true || streamOptions["include_usage"] != true {
		t.Fatalf("stream request = %+v", capturedBody)
	}
	wantDeltas := []llm.StreamDelta{
		{Kind: "reasoning", Index: 0, Text: "plan "},
		{Kind: "text", Index: 0, Text: "hel"},
		{Kind: "text", Index: 0, Text: "lo"},
	}
	if !slices.Equal(deltas, wantDeltas) {
		t.Fatalf("deltas = %+v, want %+v", deltas, wantDeltas)
	}
	if resp.Message.FirstText() != "hello" || resp.StopReason != llm.StopToolUse {
		t.Fatalf("response = %+v", resp)
	}
	if calls := resp.Message.ToolCalls(); len(calls) != 1 || calls[0].ToolName != "read" || calls[0].Input["path"] != "x" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if len(resp.Message.Blocks) < 2 || resp.Message.Blocks[0].Type != llm.BlockReasoning || resp.Message.Blocks[0].Text != "plan " {
		t.Fatalf("reasoning response = %+v", resp.Message.Blocks)
	}
	if resp.Usage.InputTokens != 9 || resp.Usage.OutputTokens != 3 || resp.Usage.CachedInputTokens != 4 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestOpenAI_StreamIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestProfile(t, providerprofile.Config{Protocol: string(llm.ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
	_, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{StreamIdleTimeout: 20 * time.Millisecond})
	openaiRequireStreamIdleTimeout(t, err)
}

func TestOpenAI_ToolWithoutPropertiesUsesEmptyObject(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{
		Protocol: string(llm.ProtocolOpenAIChat),
		BaseURL:  srv.URL,
		APIKey:   "k",
		Model:    "m",
	}), nil)
	schema := map[string]any{"type": "object"}

	if _, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, []llm.ToolSpec{
		{Name: "list_agents", Description: "list agents", Schema: schema},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, mutated := schema["properties"]; mutated {
		t.Fatalf("input schema was mutated: %+v", schema)
	}
	tools, _ := capturedBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", capturedBody["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	fn, _ := tool["function"].(map[string]any)
	params, _ := fn["parameters"].(map[string]any)
	openaiAssertEmptyProperties(t, params)
}

func TestOpenAI_ReplaysNoArgumentToolUseAsEmptyObject(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{
		Protocol: string(llm.ProtocolOpenAIChat),
		BaseURL:  srv.URL,
		APIKey:   "k",
		Model:    "m",
	}), nil)
	history := []llm.Message{
		llm.TextMessage(llm.RoleUser, "check status"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type:      llm.BlockToolUse,
			ToolUseID: "call_1",
			ToolName:  "mcp__chanwire__chanwire_status",
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_1", Content: "ok"}}},
		llm.TextMessage(llm.RoleUser, "hello"),
	}

	if _, err := p.Complete(context.Background(), "", history, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	msgs, _ := capturedBody["messages"].([]any)
	for _, raw := range msgs {
		msg, _ := raw.(map[string]any)
		if msg["role"] != "assistant" {
			continue
		}
		calls, _ := msg["tool_calls"].([]any)
		if len(calls) != 1 {
			t.Fatalf("tool_calls = %+v", msg["tool_calls"])
		}
		call, _ := calls[0].(map[string]any)
		fn, _ := call["function"].(map[string]any)
		if fn["arguments"] != "{}" {
			t.Fatalf("arguments = %q, want {}", fn["arguments"])
		}
		return
	}
	t.Fatalf("assistant tool call message not found: %+v", msgs)
}

func TestOpenAICodexResponses_RetriesTransportError(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, fmt.Errorf("net/http: TLS handshake timeout")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`data: {"type":"response.completed","response":{"id":"resp_1","model":"m","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}}` + "\n\n",
			)),
			Request: r,
		}, nil
	})}
	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{ID: "openai-codex", Protocol: string(llm.ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)

	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if resp.Message.FirstText() != "ok" {
		t.Fatalf("text = %q, want ok", resp.Message.FirstText())
	}
}

func TestOpenAICodexResponses_RetriesSSEReadEOF(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       openaiErrReadCloser{err: io.ErrUnexpectedEOF},
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				`data: {"type":"response.completed","response":{"id":"resp_1","model":"m","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}}` + "\n\n",
			)),
			Request: r,
		}, nil
	})}
	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{ID: "openai-codex", Protocol: string(llm.ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)

	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if resp.Message.FirstText() != "ok" {
		t.Fatalf("text = %q, want ok", resp.Message.FirstText())
	}
}

func TestOpenAICodexResponses_StreamIdleTimeoutIsDeadline(t *testing.T) {
	oldDelay := codexSSERetryBaseDelay
	codexSSERetryBaseDelay = 0
	t.Cleanup(func() { codexSSERetryBaseDelay = oldDelay })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{
		ID:       "openai-codex",
		Protocol: string(llm.ProtocolOpenAICodexResponses),
		BaseURL:  srv.URL,
		APIKey:   "k",
		Model:    "m",
	}), nil)
	_, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		StreamIdleTimeout: 20 * time.Millisecond,
	})
	openaiRequireStreamIdleTimeout(t, err)
}

func TestOpenAICodexResponses_RetriesStreamIdleAfterEmittingDelta(t *testing.T) {
	oldDelay := codexSSERetryBaseDelay
	codexSSERetryBaseDelay = 0
	t.Cleanup(func() { codexSSERetryBaseDelay = oldDelay })

	attempts := 0
	client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			partial := `data: {"type":"response.reasoning_summary_text.delta","output_index":0,"summary_index":0,"delta":"partial plan"}` + "\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       &openaiIdleReadCloser{ctx: r.Context(), prefix: strings.NewReader(partial)},
				Request:    r,
			}, nil
		}
		return openaiCodexCompletedTextResponse(r, "recovered"), nil
	})}
	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{ID: "openai-codex", Protocol: string(llm.ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)
	var diagnostics []llm.ProviderRetryDiagnostic
	var deltas []llm.StreamDelta

	resp, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		StreamIdleTimeout: 20 * time.Millisecond,
		OnDelta:           func(delta llm.StreamDelta) { deltas = append(deltas, delta) },
		RetryObserver:     func(d llm.ProviderRetryDiagnostic) { diagnostics = append(diagnostics, d) },
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if attempts != 2 || resp.Message.FirstText() != "recovered" {
		t.Fatalf("attempts = %d, response = %+v", attempts, resp)
	}
	if len(deltas) != 1 || deltas[0].Kind != "reasoning" {
		t.Fatalf("deltas = %+v, want first-attempt reasoning delta", deltas)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want one retry", diagnostics)
	}
	got := diagnostics[0]
	if !got.WillRetry || got.Exhausted || got.Attempt != 1 || got.MaxAttempts != codexSSEIdleMaxAttempts || got.RetryReason != "codex_sse_idle_timeout" {
		t.Fatalf("retry diagnostic = %+v", got)
	}
}

func TestOpenAICodexResponses_RetriesSSEReadAfterEmittingDelta(t *testing.T) {
	oldDelay := codexSSERetryBaseDelay
	codexSSERetryBaseDelay = 0
	t.Cleanup(func() { codexSSERetryBaseDelay = oldDelay })

	attempts := 0
	client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			partial := `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}` + "\n\n"
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(io.MultiReader(strings.NewReader(partial), openaiErrReadCloser{err: errors.New("stream error: stream ID 11; INTERNAL_ERROR; received from peer")})),
				Request:    r,
			}, nil
		}
		return openaiCodexCompletedTextResponse(r, "recovered"), nil
	})}
	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{ID: "openai-codex", Protocol: string(llm.ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)
	withOpts := p.(llm.ProviderWithOptions)
	var diagnostics []llm.ProviderRetryDiagnostic
	var deltas []llm.StreamDelta

	resp, err := withOpts.CompleteWithOptions(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		OnDelta: func(delta llm.StreamDelta) {
			deltas = append(deltas, delta)
		},
		RetryObserver: func(d llm.ProviderRetryDiagnostic) {
			diagnostics = append(diagnostics, d)
		},
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if attempts != 2 || resp.Message.FirstText() != "recovered" {
		t.Fatalf("attempts = %d, response = %+v", attempts, resp)
	}
	want := []llm.StreamDelta{{Kind: "text", Index: 0, Text: "partial"}}
	if !slices.Equal(deltas, want) {
		t.Fatalf("deltas = %+v, want %+v", deltas, want)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want one retry", diagnostics)
	}
	got := diagnostics[0]
	if !got.WillRetry || got.Exhausted || got.Attempt != 1 || got.MaxAttempts != protocolsupport.ProviderMaxRetries+1 || got.RetryReason != "codex_sse_read" {
		t.Fatalf("retry diagnostic = %+v", got)
	}
}

func TestOpenAICodexResponses_RetriesSSEReadInternalErrorByCategory(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       openaiErrReadCloser{err: errors.New("stream error: stream ID 37; INTERNAL_ERROR; received from peer")},
				Request:    r,
			}, nil
		}
		return openaiCodexCompletedTextResponse(r, "ok"), nil
	})}
	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{ID: "openai-codex", Protocol: string(llm.ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)
	withOpts, ok := p.(llm.ProviderWithOptions)
	if !ok {
		t.Fatal("openai-codex provider does not implement ProviderWithOptions")
	}
	var diagnostics []llm.ProviderRetryDiagnostic

	resp, err := withOpts.CompleteWithOptions(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		RetryObserver: func(d llm.ProviderRetryDiagnostic) {
			diagnostics = append(diagnostics, d)
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if resp.Message.FirstText() != "ok" {
		t.Fatalf("text = %q, want ok", resp.Message.FirstText())
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics len = %d, want 1: %+v", len(diagnostics), diagnostics)
	}
	got := diagnostics[0]
	if !got.WillRetry || got.Exhausted || got.Attempt != 1 || got.MaxAttempts != protocolsupport.ProviderMaxRetries+1 || got.DelayMS <= 0 {
		t.Fatalf("retry diagnostic = %+v", got)
	}
	if got.Provider != "openai-codex" || got.Model != "m" || got.Protocol != llm.ProtocolOpenAICodexResponses || got.Transport != providerprofile.CodexTransportSSE {
		t.Fatalf("provider diagnostic identity = %+v", got)
	}
	if got.RetryReason != "codex_sse_read" || !strings.Contains(got.RawError, "INTERNAL_ERROR") {
		t.Fatalf("retry diagnostic reason/error = %+v", got)
	}
}

func TestOpenAICodexResponses_DoesNotRetryContextSSEReadErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 0
			client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				attempts++
				partial := `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}` + "\n\n"
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(io.MultiReader(strings.NewReader(partial), openaiErrReadCloser{err: tc.err})),
					Request:    r,
				}, nil
			})}
			p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{ID: "openai-codex", Protocol: string(llm.ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)
			withOpts := p.(llm.ProviderWithOptions)
			var diagnostics []llm.ProviderRetryDiagnostic

			_, err := withOpts.CompleteWithOptions(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
				RetryObserver: func(d llm.ProviderRetryDiagnostic) {
					diagnostics = append(diagnostics, d)
				},
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if attempts != 1 {
				t.Fatalf("attempts = %d, want 1", attempts)
			}
			if len(diagnostics) != 0 {
				t.Fatalf("diagnostics = %+v, want none", diagnostics)
			}
		})
	}
}

func TestOpenAICodexResponses_SSEReadRetryExhaustionReportsAttempts(t *testing.T) {
	oldDelay := codexSSERetryBaseDelay
	codexSSERetryBaseDelay = 0
	t.Cleanup(func() { codexSSERetryBaseDelay = oldDelay })

	attempts := 0
	client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       openaiErrReadCloser{err: errors.New("stream error: stream ID 9; INTERNAL_ERROR; received from peer")},
			Request:    r,
		}, nil
	})}
	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{ID: "openai-codex", Protocol: string(llm.ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)
	withOpts := p.(llm.ProviderWithOptions)
	var diagnostics []llm.ProviderRetryDiagnostic

	_, err := withOpts.CompleteWithOptions(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		RetryObserver: func(d llm.ProviderRetryDiagnostic) {
			diagnostics = append(diagnostics, d)
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "codex SSE retry exhausted after 11 attempts") || !strings.Contains(err.Error(), "INTERNAL_ERROR") {
		t.Fatalf("err = %v", err)
	}
	if attempts != protocolsupport.ProviderMaxRetries+1 {
		t.Fatalf("attempts = %d, want %d", attempts, protocolsupport.ProviderMaxRetries+1)
	}
	if len(diagnostics) != protocolsupport.ProviderMaxRetries+1 {
		t.Fatalf("diagnostics len = %d, want %d: %+v", len(diagnostics), protocolsupport.ProviderMaxRetries+1, diagnostics)
	}
	final := diagnostics[len(diagnostics)-1]
	if final.WillRetry || !final.Exhausted || final.Attempt != protocolsupport.ProviderMaxRetries+1 || final.DelayMS != 0 {
		t.Fatalf("final diagnostic = %+v", final)
	}
	if reason, eligible := llm.ClassifyFallbackError(err); !eligible || reason != llm.FallbackFailureTransient {
		t.Fatalf("ClassifyFallbackError() = %q, %v, want transient fallback", reason, eligible)
	}
}

func TestOpenAICodexResponses_DoesNotRetrySemanticResponseFailure(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}` + "\n\n" +
					`data: {"type":"response.failed","response":{"id":"resp_1","model":"m","status":"failed","error":{"message":"semantic failure","code":"server_error"}}}` + "\n\n",
			)),
			Request: r,
		}, nil
	})}
	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{ID: "openai-codex", Protocol: string(llm.ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)

	_, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err == nil || !strings.Contains(err.Error(), "semantic failure") {
		t.Fatalf("err = %v, want semantic failure", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestOpenAICodexResponses_RetriesStreamClosedBeforeCompleted(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				`data: {"type":"response.completed","response":{"id":"resp_1","model":"m","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}}` + "\n\n",
			)),
			Request: r,
		}, nil
	})}
	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{ID: "openai-codex", Protocol: string(llm.ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)

	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
	if resp.Message.FirstText() != "ok" {
		t.Fatalf("text = %q, want ok", resp.Message.FirstText())
	}
}

func TestOpenAICodexResponses_EmitsOutputTextDeltas(t *testing.T) {
	client := &http.Client{Transport: openaiRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"hi "}` + "\n\n" +
					`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"there"}` + "\n\n" +
					`data: {"type":"response.completed","response":{"id":"resp_1","model":"m","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi there","annotations":[]}]}]}}` + "\n\n",
			)),
			Request: r,
		}, nil
	})}
	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{ID: "openai-codex", Protocol: string(llm.ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)
	withOpts := p.(llm.ProviderWithOptions)
	var deltas []llm.StreamDelta

	resp, err := withOpts.CompleteWithOptions(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		OnDelta: func(delta llm.StreamDelta) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if resp.Message.FirstText() != "hi there" {
		t.Fatalf("text = %q, want hi there", resp.Message.FirstText())
	}
	want := []llm.StreamDelta{
		{Kind: "text", Index: 0, Text: "hi "},
		{Kind: "text", Index: 0, Text: "there"},
	}
	if !slices.Equal(deltas, want) {
		t.Fatalf("deltas = %+v, want %+v", deltas, want)
	}
}

func TestOpenAI_CompactsEmptyHistoryMessages(t *testing.T) {
	type wireReq struct {
		Messages []map[string]any `json:"messages"`
	}
	var captured wireReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{Protocol: string(llm.ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
	hist := []llm.Message{
		llm.TextMessage(llm.RoleUser, "hello"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{}},
		{Role: llm.RoleAssistant, Blocks: nil},
		llm.TextMessage(llm.RoleUser, "again"),
		{Role: llm.RoleSystem, Blocks: nil},
	}
	if _, err := p.Complete(context.Background(), "", hist, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(captured.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1; messages=%+v", len(captured.Messages), captured.Messages)
	}
	if captured.Messages[0]["role"] != "user" {
		t.Fatalf("first message = %+v, want user", captured.Messages[0])
	}
	if captured.Messages[0]["content"] != "hello\nagain" {
		t.Fatalf("first content = %+v, want merged user text", captured.Messages[0]["content"])
	}
}

func TestOpenAI_ReplaysReasoningOnlyAssistantWithStringContent(t *testing.T) {
	type wireReq struct {
		Messages []map[string]any `json:"messages"`
	}
	var captured wireReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{Protocol: string(llm.ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
	hist := []llm.Message{
		llm.TextMessage(llm.RoleUser, "hello"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockReasoning, Text: "thought"}}},
		llm.TextMessage(llm.RoleUser, "again"),
	}
	if _, err := p.Complete(context.Background(), "", hist, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(captured.Messages) != 3 {
		t.Fatalf("messages len = %d, want 3; messages=%+v", len(captured.Messages), captured.Messages)
	}
	if captured.Messages[1]["role"] != "assistant" {
		t.Fatalf("middle message = %+v, want assistant", captured.Messages[1])
	}
	if captured.Messages[1]["content"] != "" {
		t.Fatalf("assistant content = %#v, want empty string", captured.Messages[1]["content"])
	}
	if captured.Messages[1]["reasoning"] != "thought" {
		t.Fatalf("assistant reasoning = %#v, want thought", captured.Messages[1]["reasoning"])
	}
}

func TestOpenAI_ToolResultRoundTrip(t *testing.T) {
	// Verify that tool_result blocks become role=tool messages, with the
	// matching tool_call_id, when sent back through history.
	type wireReq struct {
		Messages []map[string]any `json:"messages"`
	}
	var captured wireReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{Protocol: string(llm.ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)

	hist := []llm.Message{
		llm.TextMessage(llm.RoleUser, "do it"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockText, Text: "ok"},
			{Type: llm.BlockToolUse, ToolUseID: "call_1", ToolName: "read", Input: map[string]any{"path": "x"}},
		}},
		{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "call_1", Content: "file contents"},
		}},
	}
	if _, err := p.Complete(context.Background(), "", hist, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// Expect 3 messages: user + assistant + tool
	if len(captured.Messages) != 3 {
		t.Fatalf("got %d messages: %+v", len(captured.Messages), captured.Messages)
	}
	if captured.Messages[2]["role"] != "tool" || captured.Messages[2]["tool_call_id"] != "call_1" {
		t.Errorf("tool message wrong: %+v", captured.Messages[2])
	}
	if captured.Messages[1]["role"] != "assistant" {
		t.Errorf("assistant message wrong: %+v", captured.Messages[1])
	}
	tcs, _ := captured.Messages[1]["tool_calls"].([]any)
	if len(tcs) != 1 {
		t.Errorf("expected 1 tool call, got %+v", tcs)
	}
}

func TestOpenAI_ProjectsUserAndToolResultImages(t *testing.T) {
	media, mediaDir := openaiTestImageMedia(t)
	type wireReq struct {
		Messages []map[string]any `json:"messages"`
	}
	var captured wireReq
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{
		Protocol:     string(llm.ProtocolOpenAIChat),
		BaseURL:      srv.URL,
		APIKey:       "k",
		Model:        "m",
		MediaDir:     mediaDir,
		Capabilities: llm.CapabilityOverrides{Vision: openaiBoolPtr(true)},
	}), nil)
	hist := []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "look"}, {Type: llm.BlockImage, Media: media}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: "call_1", ToolName: "render_chart", Input: map[string]any{"id": "x"}},
			{Type: llm.BlockToolUse, ToolUseID: "call_2", ToolName: "read", Input: map[string]any{"path": "x"}},
		}},
		{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: "call_1", ToolName: "render_chart", Content: "chart", Media: media},
			{Type: llm.BlockToolResult, ToolUseID: "call_2", ToolName: "read", Content: "file"},
		}},
	}
	if _, err := p.Complete(context.Background(), "", hist, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(captured.Messages) != 5 {
		t.Fatalf("messages len = %d, messages=%+v", len(captured.Messages), captured.Messages)
	}
	userParts, _ := captured.Messages[0]["content"].([]any)
	if len(userParts) != 2 || userParts[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("first user content = %+v", captured.Messages[0]["content"])
	}
	if imageURL, _ := userParts[1].(map[string]any)["image_url"].(map[string]any); !strings.HasPrefix(fmt.Sprint(imageURL["url"]), "data:image/png;base64,") {
		t.Fatalf("image_url = %+v", imageURL)
	}
	if captured.Messages[2]["role"] != "tool" || !strings.Contains(fmt.Sprint(captured.Messages[2]["content"]), "tool_result_image") {
		t.Fatalf("tool message = %+v", captured.Messages[2])
	}
	if captured.Messages[3]["role"] != "tool" || captured.Messages[3]["tool_call_id"] != "call_2" {
		t.Fatalf("second tool message should stay contiguous before synthetic user image: %+v", captured.Messages[3])
	}
	syntheticParts, _ := captured.Messages[4]["content"].([]any)
	if len(syntheticParts) != 2 || syntheticParts[0].(map[string]any)["text"] != "Tool result image from render_chart (call_1)." || syntheticParts[1].(map[string]any)["type"] != "image_url" {
		t.Fatalf("synthetic user image message = %+v", captured.Messages[4])
	}
	if strings.Contains(fmt.Sprint(captured.Messages), "cannot view image content") {
		t.Fatalf("vision-capable chat request contains unavailable-image guidance: %+v", captured.Messages)
	}
}

func TestOpenAI_VisionCapableToolResultWarnsWhenImageUnavailable(t *testing.T) {
	_, artifactDir := openaiTestImageMedia(t)
	var captured struct {
		Messages []map[string]any `json:"messages"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &captured)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{
		Protocol:     string(llm.ProtocolOpenAIChat),
		BaseURL:      srv.URL,
		APIKey:       "k",
		Model:        "m",
		MediaDir:     artifactDir,
		Capabilities: llm.CapabilityOverrides{Vision: openaiBoolPtr(true)},
	}), nil)
	missing := &llm.MediaRef{ArtifactPath: "missing.png", MediaType: "image/png", SHA256: strings.Repeat("0", 64), OriginalBytes: 10}
	hist := []llm.Message{
		llm.TextMessage(llm.RoleUser, "look"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "call_1", ToolName: "read", Input: map[string]any{"path": "missing.png"}}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_1", ToolName: "read", Content: "image", Media: missing}}},
	}
	if _, err := p.Complete(context.Background(), "", hist, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !strings.Contains(fmt.Sprint(captured.Messages), "cannot view image content") {
		t.Fatalf("unavailable tool result image missing guidance: %+v", captured.Messages)
	}
}

func TestOpenAICodexResponses_RejectsUnsupportedResolvedTransport(t *testing.T) {
	p := NewOpenAICodexResponses(llm.ProviderProfile{
		ID:       "openai-codex",
		Protocol: llm.ProtocolOpenAICodexResponses,
		APIKey:   "k",
		Model:    "m",
		Compat:   llm.CompatOptions{CodexTransport: "bogus"},
	}, nil)
	_, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err == nil || !strings.Contains(err.Error(), `unsupported codex transport "bogus"`) {
		t.Fatalf("err = %v, want unsupported codex transport", err)
	}
}

func TestExtractReasoningContent(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"deepseek":  {`{"role":"assistant","content":"hi","reasoning_content":"deepseek thoughts"}`, "deepseek thoughts"},
		"ollama":    {`{"role":"assistant","content":"hi","reasoning":"ollama thoughts"}`, "ollama thoughts"},
		"thinking":  {`{"role":"assistant","content":"hi","thinking":"plain thinking"}`, "plain thinking"},
		"none":      {`{"role":"assistant","content":"hi"}`, ""},
		"empty":     {``, ""},
		"prefer-rc": {`{"reasoning_content":"a","reasoning":"b","thinking":"c"}`, "a"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := extractReasoningContent(tc.in); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestOpenAI_ThinkingEffort(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{
		Protocol:       string(llm.ProtocolOpenAIChat),
		BaseURL:        srv.URL,
		APIKey:         "k",
		Model:          "m",
		ThinkingEffort: "low",
	}), nil)
	if _, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if capturedBody["reasoning_effort"] != "low" {
		t.Errorf("reasoning_effort = %v, want %q", capturedBody["reasoning_effort"], "low")
	}
}

func TestOpenAI_CompleteOptionsOverrideDeepSeekThinkingEffort(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{
		ID:             "deepseek",
		BaseURL:        srv.URL,
		APIKey:         "k",
		Model:          "deepseek-v4-pro",
		ThinkingEffort: "max",
	}), nil)
	if _, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{ThinkingEffort: "low"}); err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if capturedBody["reasoning_effort"] != "low" {
		t.Errorf("reasoning_effort = %v, want request override %q", capturedBody["reasoning_effort"], "low")
	}
}

func TestOpenAI_NoThinkingEffort(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{Protocol: string(llm.ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
	if _, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, present := capturedBody["reasoning_effort"]; present {
		t.Errorf("reasoning_effort should be absent when not configured, got %v", capturedBody["reasoning_effort"])
	}
}

func TestOpenAI_CompleteOptionsUsesOneMaxTokenField(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{Protocol: string(llm.ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
	withOpts, ok := p.(llm.ProviderWithOptions)
	if !ok {
		t.Fatal("openai provider does not implement ProviderWithOptions")
	}
	if _, err := withOpts.CompleteWithOptions(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		Purpose:         "compaction",
		MaxOutputTokens: 1234,
	}); err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if capturedBody["max_completion_tokens"] != float64(1234) {
		t.Fatalf("max_completion_tokens = %v, want 1234", capturedBody["max_completion_tokens"])
	}
	if _, present := capturedBody["max_tokens"]; present {
		t.Fatalf("max_tokens should be absent when max_completion_tokens is set: %+v", capturedBody)
	}
}

func TestOpenAI_CompleteOptionsSendsPromptCacheKeyAndRecordsCachedTokens(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"chatcmpl_1","object":"chat.completion","model":"gpt-test",
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],
			"usage":{"prompt_tokens":0,"completion_tokens":5,"total_tokens":5,"prompt_tokens_details":{"cached_tokens":80}}
		}`))
	}))
	defer srv.Close()

	p, err := newTestProvider(openaiBlockingConfig(providerprofile.Config{ID: "openai-chat-test", Protocol: "openai/chat", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.CompleteWithOptions(context.Background(), p, "sys", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		CachePolicy: llm.CachePolicy{StablePrefixKey: "juex-cache-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedBody["prompt_cache_key"] != "juex-cache-key" {
		t.Fatalf("prompt_cache_key = %+v", capturedBody["prompt_cache_key"])
	}
	if resp.Usage.InputTokens != 80 || resp.Usage.CachedInputTokens != 80 {
		t.Fatalf("usage = %+v, want input=80 cached=80", resp.Usage)
	}
}

func TestOpenAI_CapabilityGateOmitsUnsupportedParams(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	disabled := false
	p := NewOpenAI(openaiTestBlockingProfile(t, providerprofile.Config{
		Protocol:       string(llm.ProtocolOpenAIChat),
		BaseURL:        srv.URL,
		APIKey:         "k",
		Model:          "m",
		ThinkingEffort: "max",
		Capabilities: llm.CapabilityOverrides{
			Tools:           &disabled,
			ReasoningEffort: &disabled,
			ReasoningReplay: &disabled,
			MaxOutputTokens: &disabled,
		},
	}), nil)
	withOpts, ok := p.(llm.ProviderWithOptions)
	if !ok {
		t.Fatal("openai provider does not implement ProviderWithOptions")
	}
	history := []llm.Message{
		llm.TextMessage(llm.RoleUser, "do it"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockReasoning, Text: "hidden"},
			{Type: llm.BlockToolUse, ToolUseID: "call_1", ToolName: "read", Input: map[string]any{"path": "x"}},
		}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_1", Content: "file"}}},
	}
	if _, err := withOpts.CompleteWithOptions(context.Background(), "", history, []llm.ToolSpec{{Name: "read"}}, llm.CompleteOptions{MaxOutputTokens: 123}); err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	for _, key := range []string{"tools", "reasoning_effort", "max_completion_tokens"} {
		if _, present := capturedBody[key]; present {
			t.Fatalf("%s should be absent when capability disabled: %+v", key, capturedBody)
		}
	}
	msgs, _ := capturedBody["messages"].([]any)
	for _, raw := range msgs {
		msg, _ := raw.(map[string]any)
		if msg["role"] == "tool" {
			t.Fatalf("tool history should be omitted when tools are disabled: %+v", msgs)
		}
	}
}

func TestOpenAIResponses_RoundTrip(t *testing.T) {
	var capturedBody map[string]any
	var capturedHeader string
	var capturedQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		capturedHeader = r.Header.Get("X-Juex-Test")
		capturedQuery = r.URL.Query().Get("trace")
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"resp_1","object":"response","model":"gpt-test","status":"completed",
			"output":[
				{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thought summary"}],"encrypted_content":"enc"},
				{"type":"reasoning","id":"rs_2","summary":[{"type":"summary_text","text":"second thought"}],"encrypted_content":"enc_2"},
				{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]},
				{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"x\"}","status":"completed"}
			],
			"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}
		}`))
	}))
	defer srv.Close()

	p, err := newTestProvider(openaiBlockingConfig(providerprofile.Config{
		ID:             "openai",
		Protocol:       "openai/responses",
		BaseURL:        srv.URL,
		APIKey:         "k",
		Model:          "gpt-test",
		ThinkingEffort: "max",
		Headers:        map[string]string{"X-Juex-Test": "yes"},
		Query:          map[string]string{"trace": "1"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.CompleteWithOptions(context.Background(), p, "system",
		[]llm.Message{
			llm.TextMessage(llm.RoleUser, "hello"),
			{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockReasoning, Text: "prior", Signature: "rs_prev", Content: "enc_prev", Redacted: true},
				{Type: llm.BlockToolUse, ToolUseID: "call_prev", ToolName: "read", Input: map[string]any{"path": "old"}},
			}},
			{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_prev", Content: "old file"}}},
		},
		[]llm.ToolSpec{{Name: "read", Description: "read a file", Schema: map[string]any{"type": "object"}}},
		llm.CompleteOptions{ThinkingEffort: "low"},
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if capturedHeader != "yes" || capturedQuery != "1" {
		t.Fatalf("header/query = %q/%q", capturedHeader, capturedQuery)
	}
	if capturedBody["model"] != "gpt-test" || capturedBody["instructions"] != "system" {
		t.Fatalf("captured body = %+v", capturedBody)
	}
	reasoning, _ := capturedBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" || reasoning["summary"] != "auto" || capturedBody["include"] == nil {
		t.Fatalf("responses request should include reasoning controls: %+v", capturedBody)
	}
	tools, _ := capturedBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", capturedBody["tools"])
	}
	input, _ := capturedBody["input"].([]any)
	if len(input) < 4 {
		t.Fatalf("input = %+v", input)
	}
	if resp.StopReason != llm.StopToolUse {
		t.Fatalf("stop reason = %s, want tool_use", resp.StopReason)
	}
	if resp.Message.FirstText() != "hello" {
		t.Fatalf("text = %q", resp.Message.FirstText())
	}
	if calls := resp.Message.ToolCalls(); len(calls) != 1 || calls[0].ToolName != "read" || calls[0].Input["path"] != "x" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if len(resp.Message.Blocks) < 2 || resp.Message.Blocks[0].Type != llm.BlockReasoning || resp.Message.Blocks[0].Signature != "rs_1" {
		t.Fatalf("reasoning block not preserved: %+v", resp.Message.Blocks)
	}
	if resp.Message.Blocks[0].Text != "thought summary" || !resp.Message.Blocks[0].Redacted || resp.Message.Blocks[0].Content == "" {
		t.Fatalf("reasoning summary/replay metadata = %+v", resp.Message.Blocks[0])
	}
	if got := resp.Message.Blocks[1]; got.Type != llm.BlockReasoning || got.Signature != "rs_2" || got.Text != "second thought" || got.Content != "enc_2" || !got.Redacted {
		t.Fatalf("second reasoning item not preserved independently: %+v", resp.Message.Blocks)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestOpenAIResponses_ReasoningSummaryFollowsEffortGateIndependentlyOfReplay(t *testing.T) {
	disabled := false
	tests := []struct {
		name            string
		thinkingEffort  string
		reasoningEffort *bool
		reasoningReplay *bool
		wantReasoning   bool
		wantInclude     bool
	}{
		{
			name:            "summary without replay",
			thinkingEffort:  "high",
			reasoningReplay: &disabled,
			wantReasoning:   true,
		},
		{
			name:        "replay without configured effort",
			wantInclude: true,
		},
		{
			name:            "replay with disabled effort capability",
			thinkingEffort:  "high",
			reasoningEffort: &disabled,
			wantInclude:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(body, &capturedBody)
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"id":"resp_1","object":"response","model":"gpt-test","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`)
			}))
			defer srv.Close()

			p, err := newTestProvider(openaiBlockingConfig(providerprofile.Config{
				ID:             "openai",
				Protocol:       "openai/responses",
				BaseURL:        srv.URL,
				APIKey:         "k",
				Model:          "gpt-test",
				ThinkingEffort: tt.thinkingEffort,
				Capabilities: llm.CapabilityOverrides{
					ReasoningEffort: tt.reasoningEffort,
					ReasoningReplay: tt.reasoningReplay,
				},
			}))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil); err != nil {
				t.Fatal(err)
			}

			reasoning, hasReasoning := capturedBody["reasoning"].(map[string]any)
			if hasReasoning != tt.wantReasoning {
				t.Fatalf("reasoning present = %v, want %v; body=%+v", hasReasoning, tt.wantReasoning, capturedBody)
			}
			if hasReasoning && (reasoning["effort"] != tt.thinkingEffort || reasoning["summary"] != "auto") {
				t.Fatalf("reasoning = %+v", reasoning)
			}
			_, hasInclude := capturedBody["include"]
			if hasInclude != tt.wantInclude {
				t.Fatalf("include present = %v, want %v; body=%+v", hasInclude, tt.wantInclude, capturedBody)
			}
		})
	}
}

func TestOpenAIResponses_StreamsReasoningAndTextDeltas(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"plan "}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"hel"}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"lo"}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"plan"}]},{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}}`+"\n\n")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{ID: "openai", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []llm.StreamDelta
	resp, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		OnDelta: func(delta llm.StreamDelta) { deltas = append(deltas, delta) },
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if capturedBody["stream"] != true {
		t.Fatalf("stream request = %+v", capturedBody)
	}
	wantDeltas := []llm.StreamDelta{
		{Kind: "reasoning", Index: 0, Text: "plan "},
		{Kind: "text", Index: 1, Text: "hel"},
		{Kind: "text", Index: 1, Text: "lo"},
	}
	if !slices.Equal(deltas, wantDeltas) {
		t.Fatalf("deltas = %+v, want %+v", deltas, wantDeltas)
	}
	if resp.Message.FirstText() != "hello" || resp.Usage.InputTokens != 7 || resp.Usage.OutputTokens != 2 {
		t.Fatalf("response = %+v", resp)
	}
}

func TestOpenAIResponses_StopsAtCompletedEventAndBackfillsDoneItems(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n\n")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{ID: "openai", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Message.FirstText() != "ok" {
		t.Fatalf("text = %q, want ok", resp.Message.FirstText())
	}
}

func TestOpenAIResponses_StopsAtIncompleteEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.incomplete","response":{"id":"resp_1","model":"gpt-test","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","id":"msg_1","role":"assistant","status":"incomplete","content":[{"type":"output_text","text":"partial","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n\n")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{ID: "openai", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.StopReason != llm.StopMaxTokens || resp.Message.FirstText() != "partial" {
		t.Fatalf("response = %+v, want max_tokens with partial text", resp)
	}
}

func TestOpenAIResponses_TruncatedEventBeforeTerminalFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\n\n")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{ID: "openai", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err == nil || !strings.Contains(err.Error(), "unexpected end of JSON input") {
		t.Fatalf("err = %v, want truncated JSON error", err)
	}
}

func TestOpenAIResponses_StreamIdleTimeout(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{ID: "openai", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics []llm.ProviderRetryDiagnostic
	_, err = llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		StreamIdleTimeout: 20 * time.Millisecond,
		RetryObserver:     func(d llm.ProviderRetryDiagnostic) { diagnostics = append(diagnostics, d) },
	})
	openaiRequireStreamIdleTimeout(t, err)
	if !strings.Contains(err.Error(), "retry exhausted after 2 attempts") {
		t.Fatalf("err = %v, want retry exhaustion detail", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	if len(diagnostics) != 2 {
		t.Fatalf("diagnostics = %+v, want retry and exhaustion", diagnostics)
	}
	if first := diagnostics[0]; !first.WillRetry || first.Exhausted || first.Attempt != 1 {
		t.Fatalf("first diagnostic = %+v", first)
	}
	if final := diagnostics[1]; final.WillRetry || !final.Exhausted || final.Attempt != 2 || final.MaxAttempts != 2 || final.DelayMS != 0 {
		t.Fatalf("final diagnostic = %+v", final)
	}
}

func TestOpenAIResponses_RetriesStreamIdleTimeout(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"recovered","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{ID: "openai", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	var diagnostics []llm.ProviderRetryDiagnostic
	resp, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		StreamIdleTimeout: 20 * time.Millisecond,
		RetryObserver:     func(d llm.ProviderRetryDiagnostic) { diagnostics = append(diagnostics, d) },
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	if got := resp.Message.FirstText(); got != "recovered" {
		t.Fatalf("text = %q, want recovered", got)
	}
	if len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %+v, want one retry", diagnostics)
	}
	got := diagnostics[0]
	if !got.WillRetry || got.Exhausted || got.Attempt != 1 || got.MaxAttempts != 2 || got.RetryReason != "openai_responses_stream_idle_timeout" {
		t.Fatalf("retry diagnostic = %+v", got)
	}
	if got.Provider != "openai" || got.Model != "gpt-test" || got.Protocol != llm.ProtocolOpenAIResponses || got.Transport != "sse" || got.Operation != "responses.sse" {
		t.Fatalf("retry diagnostic identity = %+v", got)
	}
}

func TestOpenAIResponses_ProjectsUserImageAndToolResultImageReference(t *testing.T) {
	media, mediaDir := openaiTestImageMedia(t)
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"resp_1","object":"response","model":"gpt-test","status":"completed",
			"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`))
	}))
	defer srv.Close()

	p, err := newTestProvider(openaiBlockingConfig(providerprofile.Config{
		ID:           "openai",
		Protocol:     "openai/responses",
		BaseURL:      srv.URL,
		APIKey:       "k",
		Model:        "gpt-test",
		MediaDir:     mediaDir,
		Capabilities: llm.CapabilityOverrides{Vision: openaiBoolPtr(true)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	hist := []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "look"}, {Type: llm.BlockImage, Media: media}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "call_1", ToolName: "render_chart", Input: map[string]any{"id": "x"}}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_1", ToolName: "render_chart", Content: "chart", Media: media}}},
	}
	if _, err := p.Complete(context.Background(), "", hist, []llm.ToolSpec{{Name: "render_chart", Schema: map[string]any{"type": "object"}}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	input, _ := capturedBody["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("input = %+v", input)
	}
	content, _ := input[0].(map[string]any)["content"].([]any)
	if len(content) != 2 || content[0].(map[string]any)["type"] != "input_text" || content[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("responses image input content = %+v", content)
	}
	if !strings.HasPrefix(fmt.Sprint(content[1].(map[string]any)["image_url"]), "data:image/png;base64,") {
		t.Fatalf("responses input_image = %+v", content[1])
	}
	if input[2].(map[string]any)["type"] != "function_call_output" || !strings.Contains(fmt.Sprint(input[2].(map[string]any)["output"]), "tool_result_image") {
		t.Fatalf("responses tool result output = %+v", input[2])
	}
	if strings.Contains(fmt.Sprint(input[2].(map[string]any)["output"]), "cannot view image content") {
		t.Fatalf("vision-capable responses tool result contains unavailable-image guidance: %+v", input[2])
	}
	toolImageMessage, _ := input[3].(map[string]any)
	if toolImageMessage["role"] != "user" {
		t.Fatalf("responses tool result image message = %+v", toolImageMessage)
	}
	toolImageContent, _ := toolImageMessage["content"].([]any)
	if len(toolImageContent) != 2 || toolImageContent[0].(map[string]any)["type"] != "input_text" || toolImageContent[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("responses tool result image content = %+v", toolImageContent)
	}
	if !strings.Contains(fmt.Sprint(toolImageContent[0].(map[string]any)["text"]), "render_chart") {
		t.Fatalf("responses tool result image attribution = %+v", toolImageContent[0])
	}
	if !strings.HasPrefix(fmt.Sprint(toolImageContent[1].(map[string]any)["image_url"]), "data:image/png;base64,") {
		t.Fatalf("responses tool result input_image = %+v", toolImageContent[1])
	}
	if strings.Contains(fmt.Sprint(input), "cannot view image content") {
		t.Fatalf("vision-capable responses request contains unavailable-image guidance: %+v", input)
	}
}

func TestOpenAICodexResponses_ProjectsUserAndToolResultImages(t *testing.T) {
	media, artifactDir := openaiTestImageMedia(t)
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
	}))
	defer srv.Close()

	p := NewOpenAICodexResponses(openaiTestProfile(t, providerprofile.Config{
		ID:           "openai-codex",
		Protocol:     string(llm.ProtocolOpenAICodexResponses),
		BaseURL:      srv.URL,
		APIKey:       "codex-token",
		Model:        "gpt-test",
		MediaDir:     artifactDir,
		Capabilities: llm.CapabilityOverrides{Vision: openaiBoolPtr(true)},
	}), nil)
	hist := []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "look"}, {Type: llm.BlockImage, Media: media}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "call_1", ToolName: "read", Input: map[string]any{"path": "image.png"}}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_1", ToolName: "read", Content: "image", Media: media}}},
	}
	if _, err := p.Complete(context.Background(), "", hist, []llm.ToolSpec{{Name: "read", Schema: map[string]any{"type": "object"}}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	input, _ := capturedBody["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("input = %+v", input)
	}
	attachmentContent, _ := input[0].(map[string]any)["content"].([]any)
	if len(attachmentContent) != 2 || attachmentContent[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("codex attachment content = %+v", attachmentContent)
	}
	if input[2].(map[string]any)["type"] != "function_call_output" || !strings.Contains(fmt.Sprint(input[2].(map[string]any)["output"]), "tool_result_image") {
		t.Fatalf("codex tool result output = %+v", input[2])
	}
	toolImageContent, _ := input[3].(map[string]any)["content"].([]any)
	if len(toolImageContent) != 2 || toolImageContent[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("codex tool result image content = %+v", toolImageContent)
	}
	if strings.Contains(fmt.Sprint(input), "cannot view image content") {
		t.Fatalf("vision-capable codex request contains unavailable-image guidance: %+v", input)
	}
}

func TestOpenAIResponses_ToolWithoutPropertiesUsesEmptyObject(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"resp_1","object":"response","model":"gpt-test","status":"completed",
			"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`))
	}))
	defer srv.Close()

	p, err := newTestProvider(openaiBlockingConfig(providerprofile.Config{
		ID:       "openai",
		Protocol: "openai/responses",
		BaseURL:  srv.URL,
		APIKey:   "k",
		Model:    "gpt-test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, []llm.ToolSpec{
		{Name: "list_agents", Description: "list agents", Schema: map[string]any{"type": "object"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	tools, _ := capturedBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", capturedBody["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	params, _ := tool["parameters"].(map[string]any)
	openaiAssertEmptyProperties(t, params)
}

func TestOpenAIResponses_CompleteOptionsSendsPromptCacheKeyAndRecordsCachedTokens(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"resp_1","object":"response","model":"gpt-test","status":"completed",
			"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"usage":{"input_tokens":0,"output_tokens":6,"total_tokens":6,"input_tokens_details":{"cached_tokens":96}}
		}`))
	}))
	defer srv.Close()

	p, err := newTestProvider(openaiBlockingConfig(providerprofile.Config{ID: "openai", Protocol: "openai/responses", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.CompleteWithOptions(context.Background(), p, "sys", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		CachePolicy: llm.CachePolicy{StablePrefixKey: "juex-cache-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedBody["prompt_cache_key"] != "juex-cache-key" {
		t.Fatalf("prompt_cache_key = %+v", capturedBody["prompt_cache_key"])
	}
	if resp.Usage.InputTokens != 96 || resp.Usage.CachedInputTokens != 96 {
		t.Fatalf("usage = %+v, want input=96 cached=96", resp.Usage)
	}
}

func TestOpenAIResponses_ReplaysReasoningWithEmptySummary(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"resp_1","object":"response","model":"gpt-test","status":"completed",
			"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`))
	}))
	defer srv.Close()

	p, err := newTestProvider(openaiBlockingConfig(providerprofile.Config{
		ID:       "openai",
		Protocol: "openai/responses",
		BaseURL:  srv.URL,
		APIKey:   "k",
		Model:    "gpt-test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), "",
		[]llm.Message{
			llm.TextMessage(llm.RoleUser, "first"),
			{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockReasoning, Signature: "rs_prev_1", Content: "enc_prev_1", Redacted: true},
				{Type: llm.BlockReasoning, Text: "second summary", Signature: "rs_prev_2", Content: "enc_prev_2", Redacted: true},
				{Type: llm.BlockText, Text: "first answer"},
			}},
			llm.TextMessage(llm.RoleUser, "second"),
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	input, _ := capturedBody["input"].([]any)
	var reasoningItems []map[string]any
	for _, item := range input {
		obj, _ := item.(map[string]any)
		if obj["type"] == "reasoning" {
			reasoningItems = append(reasoningItems, obj)
		}
	}
	if len(reasoningItems) != 2 {
		t.Fatalf("reasoning replay items = %+v, want two", reasoningItems)
	}
	if reasoningItems[0]["id"] != "rs_prev_1" || reasoningItems[0]["encrypted_content"] != "enc_prev_1" {
		t.Fatalf("first reasoning replay item = %+v", reasoningItems[0])
	}
	if summary, ok := reasoningItems[0]["summary"].([]any); !ok || len(summary) != 0 {
		t.Fatalf("first reasoning summary = %#v, want empty array", reasoningItems[0]["summary"])
	}
	if reasoningItems[1]["id"] != "rs_prev_2" || reasoningItems[1]["encrypted_content"] != "enc_prev_2" {
		t.Fatalf("second reasoning replay item = %+v", reasoningItems[1])
	}
	secondSummary, ok := reasoningItems[1]["summary"].([]any)
	if !ok || len(secondSummary) != 1 {
		t.Fatalf("second reasoning summary = %#v", reasoningItems[1]["summary"])
	}
	secondSummaryPart, ok := secondSummary[0].(map[string]any)
	if !ok || secondSummaryPart["text"] != "second summary" {
		t.Fatalf("second reasoning summary = %#v", reasoningItems[1]["summary"])
	}
}

func TestOpenAICodexResponses_RoundTrip(t *testing.T) {
	var capturedBody map[string]any
	var capturedAuth, capturedAccount, capturedOriginator, capturedBeta, capturedAccept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/codex/responses") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		capturedAuth = r.Header.Get("Authorization")
		capturedAccount = r.Header.Get("chatgpt-account-id")
		capturedOriginator = r.Header.Get("originator")
		capturedBeta = r.Header.Get("OpenAI-Beta")
		capturedAccept = r.Header.Get("Accept")
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","output":[{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"thought summary"}],"encrypted_content":"enc"},{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"x\"}","status":"completed"}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}}`+"\n\n")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{
		ID:             "openai-codex",
		Protocol:       "openai-codex/responses",
		BaseURL:        srv.URL,
		APIKey:         "codex-token",
		Model:          "gpt-test",
		ThinkingEffort: "max",
		Headers:        map[string]string{"ChatGPT-Account-ID": "acct_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.CompleteWithOptions(context.Background(), p, "system",
		[]llm.Message{
			llm.TextMessage(llm.RoleUser, "hello"),
			{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockReasoning, Text: "prior", Signature: "rs_prev", Content: "enc_prev", Redacted: true},
				{Type: llm.BlockToolUse, ToolUseID: "call_prev", ToolName: "read", Input: map[string]any{"path": "old"}},
			}},
			{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_prev", Content: "old file"}}},
		},
		[]llm.ToolSpec{{Name: "read", Description: "read a file", Schema: map[string]any{"type": "object"}}},
		llm.CompleteOptions{ThinkingEffort: "low"},
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if capturedAuth != "Bearer codex-token" || capturedAccount != "acct_1" || capturedOriginator != "juex" {
		t.Fatalf("headers auth/account/originator = %q/%q/%q", capturedAuth, capturedAccount, capturedOriginator)
	}
	if capturedBeta != "responses=experimental" || capturedAccept != "text/event-stream" {
		t.Fatalf("headers beta/accept = %q/%q", capturedBeta, capturedAccept)
	}
	if capturedBody["model"] != "gpt-test" || capturedBody["instructions"] != "system" || capturedBody["stream"] != true {
		t.Fatalf("captured body = %+v", capturedBody)
	}
	reasoning, _ := capturedBody["reasoning"].(map[string]any)
	if reasoning["effort"] != "low" || capturedBody["include"] == nil {
		t.Fatalf("codex request should include reasoning controls: %+v", capturedBody)
	}
	input, _ := capturedBody["input"].([]any)
	if len(input) < 3 {
		t.Fatalf("input = %+v", input)
	}
	if resp.StopReason != llm.StopToolUse {
		t.Fatalf("stop reason = %s, want tool_use", resp.StopReason)
	}
	if resp.Message.FirstText() != "hello" {
		t.Fatalf("text = %q", resp.Message.FirstText())
	}
	if calls := resp.Message.ToolCalls(); len(calls) != 1 || calls[0].ToolName != "read" || calls[0].Input["path"] != "x" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if len(resp.Message.Blocks) == 0 || resp.Message.Blocks[0].Type != llm.BlockReasoning || resp.Message.Blocks[0].Signature != "rs_1" {
		t.Fatalf("reasoning block not preserved: %+v", resp.Message.Blocks)
	}
	if resp.Message.Blocks[0].Text != "thought summary" || !resp.Message.Blocks[0].Redacted || resp.Message.Blocks[0].Content == "" {
		t.Fatalf("codex reasoning summary/replay metadata = %+v", resp.Message.Blocks[0])
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestOpenAICodexResponses_BoundsReplayedToolCallIDs(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	longIDs := []string{
		"mcp__wechat-wire__wechat_wire_send_message-1785160851254640548-253",
		"mcp__wechat-wire__wechat_wire_send_attachment-1785161749082587296-275",
	}
	history := []llm.Message{
		llm.TextMessage(llm.RoleUser, "send"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{
			{Type: llm.BlockToolUse, ToolUseID: longIDs[0], ToolName: "send_message", Input: map[string]any{"text": "hello"}},
			{Type: llm.BlockToolUse, ToolUseID: longIDs[1], ToolName: "send_attachment", Input: map[string]any{"path": "report.txt"}},
		}},
		{Role: llm.RoleUser, Blocks: []llm.Block{
			{Type: llm.BlockToolResult, ToolUseID: longIDs[0], Content: "sent"},
			{Type: llm.BlockToolResult, ToolUseID: longIDs[1], Content: "uploaded"},
		}},
	}
	if _, err := p.Complete(context.Background(), "", history, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	input, _ := capturedBody["input"].([]any)
	callIDs := map[string]int{}
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		switch item["type"] {
		case "function_call", "function_call_output":
			callID, _ := item["call_id"].(string)
			if len(callID) > 64 {
				t.Fatalf("Codex call_id length = %d, want <= 64: %q", len(callID), callID)
			}
			callIDs[callID]++
		}
	}
	if len(callIDs) != 2 {
		t.Fatalf("bounded call IDs = %+v, want two distinct IDs", callIDs)
	}
	for callID, count := range callIDs {
		if count != 2 {
			t.Fatalf("call_id %q appears %d times, want matching call and output", callID, count)
		}
	}
	if history[1].Blocks[0].ToolUseID != longIDs[0] || history[1].Blocks[1].ToolUseID != longIDs[1] {
		t.Fatalf("canonical history was mutated: %+v", history[1].Blocks)
	}
}

func TestOpenAIResponses_BoundsCrossProviderToolCallIDs(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{
			"id":"resp_1","object":"response","model":"gpt-test","status":"completed",
			"output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],
			"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
		}`)
	}))
	defer srv.Close()

	p, err := newTestProvider(openaiBlockingConfig(providerprofile.Config{
		ID:       "clip-local-responses",
		Protocol: "openai/responses",
		BaseURL:  srv.URL,
		APIKey:   "k",
		Model:    "gpt-test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	longID := "mcp__wechat-wire__wechat_wire_send_message-1785521763244099543-113"
	if len(longID) != 66 {
		t.Fatalf("fixture call ID length = %d, want 66", len(longID))
	}
	history := []llm.Message{
		llm.TextMessage(llm.RoleUser, "send"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{
			Type: llm.BlockToolUse, ToolUseID: longID, ToolName: "mcp__wechat-wire__wechat_wire_send_message",
			Input: map[string]any{"text": "hello"},
		}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{
			Type: llm.BlockToolResult, ToolUseID: longID, Content: "sent",
		}}},
	}
	if _, err := p.Complete(context.Background(), "", history, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	input, _ := capturedBody["input"].([]any)
	var callIDs []string
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		switch item["type"] {
		case "function_call", "function_call_output":
			callID, _ := item["call_id"].(string)
			if len(callID) > 64 {
				t.Fatalf("Responses call_id length = %d, want <= 64: %q", len(callID), callID)
			}
			callIDs = append(callIDs, callID)
		}
	}
	if len(callIDs) != 2 || callIDs[0] != callIDs[1] {
		t.Fatalf("call IDs = %q, want matching function call and output IDs", callIDs)
	}
	if history[1].Blocks[0].ToolUseID != longID || history[2].Blocks[0].ToolUseID != longID {
		t.Fatalf("canonical history was mutated: %+v", history)
	}
}

func TestBoundedOpenAIResponsesToolCallID(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want string
	}{
		{name: "empty", id: "", want: ""},
		{name: "under limit", id: "call_1", want: "call_1"},
		{name: "exact limit", id: strings.Repeat("a", 64), want: strings.Repeat("a", 64)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := boundedOpenAIResponsesToolCallID(tc.id); got != tc.want {
				t.Fatalf("bounded ID = %q, want %q", got, tc.want)
			}
		})
	}

	longIDs := []string{
		strings.Repeat("b", 65),
		strings.Repeat("c", 66),
		strings.Repeat("界", 22),
	}
	bounded := map[string]bool{}
	for _, id := range longIDs {
		got := boundedOpenAIResponsesToolCallID(id)
		if len(got) != 64 || !strings.HasPrefix(got, "juex_") {
			t.Fatalf("bounded ID = %q (%d bytes), want 64-byte juex_ ID", got, len(got))
		}
		for i := range len(got) {
			if got[i] >= 0x80 {
				t.Fatalf("bounded ID contains non-ASCII byte: %q", got)
			}
		}
		if got != boundedOpenAIResponsesToolCallID(id) {
			t.Fatalf("bounded ID is not deterministic for %q", id)
		}
		if bounded[got] {
			t.Fatalf("distinct fixture IDs collided at %q", got)
		}
		bounded[got] = true
	}
}

func TestOpenAICodexResponses_DoesNotReplayReasoningItemsWithStoreFalse(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), "",
		[]llm.Message{
			llm.TextMessage(llm.RoleUser, "first"),
			{Role: llm.RoleAssistant, Blocks: []llm.Block{
				{Type: llm.BlockReasoning, Text: "prior", Signature: "rs_prev", Content: "enc_prev", Redacted: true},
				{Type: llm.BlockText, Text: "first answer"},
			}},
			llm.TextMessage(llm.RoleUser, "second"),
		},
		nil,
	); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	if capturedBody["store"] != false {
		t.Fatalf("store = %+v, want false", capturedBody["store"])
	}
	input, _ := capturedBody["input"].([]any)
	for _, raw := range input {
		item, _ := raw.(map[string]any)
		if item["type"] == "reasoning" || item["id"] == "rs_prev" {
			t.Fatalf("codex store=false request must not replay reasoning item: %+v; input=%+v", item, input)
		}
	}
}

func TestOpenAICodexResponses_ToolWithoutPropertiesUsesEmptyObject(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`+"\n\n")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, []llm.ToolSpec{
		{Name: "list_agents", Description: "list agents", Schema: map[string]any{"type": "object"}},
	}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	tools, _ := capturedBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", capturedBody["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	params, _ := tool["parameters"].(map[string]any)
	openaiAssertEmptyProperties(t, params)
}

func TestOpenAICodexResponses_CompleteOptionsSendsPromptCacheKeyAndRecordsCachedTokens(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":0,"output_tokens":7,"total_tokens":7,"input_tokens_details":{"cached_tokens":112}}}}`+"\n\n")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{ID: "openai-codex", Protocol: "openai-codex/responses", BaseURL: srv.URL, APIKey: "codex-token", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := llm.CompleteWithOptions(context.Background(), p, "sys", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		CachePolicy: llm.CachePolicy{StablePrefixKey: "juex-cache-key", Retention: "24h"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedBody["prompt_cache_key"] != "juex-cache-key" {
		t.Fatalf("prompt_cache_key = %+v", capturedBody["prompt_cache_key"])
	}
	if capturedBody["prompt_cache_retention"] != "24h" {
		t.Fatalf("prompt_cache_retention = %+v", capturedBody["prompt_cache_retention"])
	}
	if resp.Usage.InputTokens != 112 || resp.Usage.CachedInputTokens != 112 {
		t.Fatalf("usage = %+v, want input=112 cached=112", resp.Usage)
	}
}

func TestOpenAICodexResponses_WebsocketCachedSendsCodexFrame(t *testing.T) {
	type capture struct {
		header http.Header
		url    string
		frame  map[string]any
	}
	captures := make(chan capture, 1)
	serverErrs := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !openaiIsWebsocketRequest(r) {
			serverErrs <- fmt.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "websocket expected", http.StatusBadRequest)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			serverErrs <- err
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, data, err := conn.Read(ctx)
		if err != nil {
			serverErrs <- err
			return
		}
		var frame map[string]any
		if err := json.Unmarshal(data, &frame); err != nil {
			serverErrs <- err
			return
		}
		captures <- capture{header: r.Header.Clone(), url: r.URL.String(), frame: frame}
		if err := conn.Write(ctx, websocket.MessageText, openaiCodexCompletedWebsocketEvent("resp_1", "ok")); err != nil {
			serverErrs <- err
		}
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
		Headers:  map[string]string{"ChatGPT-Account-ID": "acct_1"},
		Query:    map[string]string{"trace": "1"},
		Compat:   llm.CompatOptions{CodexTransport: providerprofile.CodexTransportWebSocketCached},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), "system", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Message.FirstText() != "ok" {
		t.Fatalf("text = %q, want ok", resp.Message.FirstText())
	}
	select {
	case err := <-serverErrs:
		t.Fatal(err)
	default:
	}
	captured := <-captures
	if got := captured.header.Get("Authorization"); got != "Bearer codex-token" {
		t.Fatalf("Authorization = %q", got)
	}
	if got := captured.header.Get("OpenAI-Beta"); got != codexResponsesWebsocketBeta {
		t.Fatalf("OpenAI-Beta = %q", got)
	}
	if got := captured.header.Get("chatgpt-account-id"); got != "acct_1" {
		t.Fatalf("chatgpt-account-id = %q", got)
	}
	if got := captured.url; got != "/codex/responses?trace=1" {
		t.Fatalf("websocket URL = %q", got)
	}
	if got := captured.frame["type"]; got != "response.create" {
		t.Fatalf("type = %v", got)
	}
	if got := captured.frame["model"]; got != "gpt-test" {
		t.Fatalf("model = %v", got)
	}
	if got := captured.frame["stream"]; got != true {
		t.Fatalf("stream = %v", got)
	}
	if got := captured.frame["store"]; got != false {
		t.Fatalf("store = %v", got)
	}
	if _, ok := captured.frame["previous_response_id"]; ok {
		t.Fatalf("first websocket frame should not include previous_response_id: %+v", captured.frame)
	}
	input, _ := captured.frame["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("input = %+v", captured.frame["input"])
	}
}

func TestOpenAICodexResponses_WebsocketEmitsDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if _, _, err := conn.Read(ctx); err != nil {
			return
		}
		for _, event := range [][]byte{
			[]byte(`{"type":"response.reasoning_summary_text.delta","item_id":"rs_1","output_index":0,"summary_index":0,"delta":"plan "}`),
			[]byte(`{"type":"response.output_text.delta","item_id":"msg_1","output_index":1,"content_index":0,"delta":"ok"}`),
			openaiCodexCompletedWebsocketEvent("resp_1", "ok"),
		} {
			if err := conn.Write(ctx, websocket.MessageText, event); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{
		ID:      "openai-codex",
		BaseURL: srv.URL,
		APIKey:  "codex-token",
		Model:   "gpt-test",
		Compat:  llm.CompatOptions{CodexTransport: providerprofile.CodexTransportWebSocket},
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []llm.StreamDelta
	resp, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		OnDelta: func(delta llm.StreamDelta) { deltas = append(deltas, delta) },
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	want := []llm.StreamDelta{
		{Kind: "reasoning", Index: 0, Text: "plan "},
		{Kind: "text", Index: 1, Text: "ok"},
	}
	if !slices.Equal(deltas, want) {
		t.Fatalf("deltas = %+v, want %+v", deltas, want)
	}
	if resp.Message.FirstText() != "ok" {
		t.Fatalf("response = %+v", resp)
	}
}

func TestOpenAICodexResponses_WebsocketIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _, _ = conn.Read(ctx)
		time.Sleep(100 * time.Millisecond)
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{
		ID:      "openai-codex",
		BaseURL: srv.URL,
		APIKey:  "codex-token",
		Model:   "gpt-test",
		Compat:  llm.CompatOptions{CodexTransport: providerprofile.CodexTransportWebSocket},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		StreamIdleTimeout: 20 * time.Millisecond,
	})
	openaiRequireStreamIdleTimeout(t, err)
}

func TestOpenAICodexResponses_WebsocketCachedUsesPreviousResponseIDForIncrementalInput(t *testing.T) {
	frames := make(chan map[string]any, 2)
	serverErrs := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !openaiIsWebsocketRequest(r) {
			serverErrs <- fmt.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "websocket expected", http.StatusBadRequest)
			return
		}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			serverErrs <- err
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		for i, event := range [][]byte{
			openaiCodexCompletedWebsocketEvent("resp_1", "first answer"),
			openaiCodexCompletedWebsocketEvent("resp_2", "second answer"),
		} {
			_, data, err := conn.Read(ctx)
			if err != nil {
				serverErrs <- fmt.Errorf("read frame %d: %w", i, err)
				return
			}
			var frame map[string]any
			if err := json.Unmarshal(data, &frame); err != nil {
				serverErrs <- err
				return
			}
			frames <- frame
			if err := conn.Write(ctx, websocket.MessageText, event); err != nil {
				serverErrs <- err
				return
			}
		}
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
		Compat:   llm.CompatOptions{CodexTransport: providerprofile.CodexTransportWebSocketCached},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstHistory := []llm.Message{llm.TextMessage(llm.RoleUser, "first")}
	firstResp, err := p.Complete(context.Background(), "", firstHistory, nil)
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	secondHistory := append(append([]llm.Message{}, firstHistory...), firstResp.Message, llm.TextMessage(llm.RoleUser, "second"))
	secondResp, err := p.Complete(context.Background(), "", secondHistory, nil)
	if err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	if secondResp.Message.FirstText() != "second answer" {
		t.Fatalf("second text = %q", secondResp.Message.FirstText())
	}
	select {
	case err := <-serverErrs:
		t.Fatal(err)
	default:
	}
	firstFrame := <-frames
	secondFrame := <-frames
	if _, ok := firstFrame["previous_response_id"]; ok {
		t.Fatalf("first frame should not include previous_response_id: %+v", firstFrame)
	}
	if got := secondFrame["previous_response_id"]; got != "resp_1" {
		t.Fatalf("previous_response_id = %v, frame=%+v", got, secondFrame)
	}
	input, _ := secondFrame["input"].([]any)
	if len(input) != 1 {
		t.Fatalf("incremental input = %+v", secondFrame["input"])
	}
	item, _ := input[0].(map[string]any)
	if item["role"] != "user" || item["content"] != "second" {
		t.Fatalf("incremental item = %+v", item)
	}
}

func TestOpenAICodexResponses_WebsocketModeDoesNotCacheConnectionOrPreviousResponse(t *testing.T) {
	handshakes := make(chan struct{}, 2)
	frames := make(chan map[string]any, 2)
	serverErrs := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !openaiIsWebsocketRequest(r) {
			serverErrs <- fmt.Errorf("unexpected HTTP request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "websocket expected", http.StatusBadRequest)
			return
		}
		handshakes <- struct{}{}
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			serverErrs <- err
			return
		}
		defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, data, err := conn.Read(ctx)
		if err != nil {
			serverErrs <- err
			return
		}
		var frame map[string]any
		if err := json.Unmarshal(data, &frame); err != nil {
			serverErrs <- err
			return
		}
		frames <- frame
		if err := conn.Write(ctx, websocket.MessageText, openaiCodexCompletedWebsocketEvent("resp_ws", "ok")); err != nil {
			serverErrs <- err
		}
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
		Compat:   llm.CompatOptions{CodexTransport: providerprofile.CodexTransportWebSocket},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstHistory := []llm.Message{llm.TextMessage(llm.RoleUser, "first")}
	firstResp, err := p.Complete(context.Background(), "", firstHistory, nil)
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	secondHistory := append(append([]llm.Message{}, firstHistory...), firstResp.Message, llm.TextMessage(llm.RoleUser, "second"))
	if _, err := p.Complete(context.Background(), "", secondHistory, nil); err != nil {
		t.Fatalf("second Complete: %v", err)
	}
	select {
	case err := <-serverErrs:
		t.Fatal(err)
	default:
	}
	<-handshakes
	<-handshakes
	firstFrame := <-frames
	secondFrame := <-frames
	if _, ok := firstFrame["previous_response_id"]; ok {
		t.Fatalf("first frame should not include previous_response_id: %+v", firstFrame)
	}
	if _, ok := secondFrame["previous_response_id"]; ok {
		t.Fatalf("websocket mode should not include previous_response_id: %+v", secondFrame)
	}
	input, _ := secondFrame["input"].([]any)
	if len(input) <= 1 {
		t.Fatalf("websocket mode should send full input, got %+v", secondFrame["input"])
	}
}

func TestOpenAICodexResponses_WebsocketAutoFallsBackToSSE(t *testing.T) {
	wsAttempts := 0
	sseAttempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if openaiIsWebsocketRequest(r) {
			wsAttempts++
			http.Error(w, "websocket unavailable", http.StatusServiceUnavailable)
			return
		}
		sseAttempts++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", openaiCodexCompletedWebsocketEvent("resp_sse", "sse ok"))
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
		Compat:   llm.CompatOptions{CodexTransport: providerprofile.CodexTransportAuto},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Message.FirstText() != "sse ok" {
		t.Fatalf("text = %q", resp.Message.FirstText())
	}
	if wsAttempts != 1 || sseAttempts != 1 {
		t.Fatalf("attempts websocket/sse = %d/%d", wsAttempts, sseAttempts)
	}
}

func TestOpenAICodexResponses_WebsocketClosedBeforeCompletedReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _, _ = conn.Read(ctx)
		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
		Compat:   llm.CompatOptions{CodexTransport: providerprofile.CodexTransportWebSocketCached},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err == nil || !strings.Contains(err.Error(), "response.completed") {
		t.Fatalf("err = %v, want response.completed close error", err)
	}
}

func openaiAssertEmptyProperties(t *testing.T, schema map[string]any) {
	t.Helper()
	openaiAssertObjectWithEmptyProperties(t, schema)
	if schema["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %+v, want false in empty object schema %+v", schema["additionalProperties"], schema)
	}
}

func openaiAssertObjectWithEmptyProperties(t *testing.T, schema map[string]any) {
	t.Helper()
	if schema["type"] != "object" {
		t.Fatalf("schema type = %v, want object in %+v", schema["type"], schema)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok || props == nil {
		t.Fatalf("properties should be an empty object, got %+v in schema %+v", schema["properties"], schema)
	}
	if len(props) != 0 {
		t.Fatalf("properties = %+v, want empty object", props)
	}
}

func openaiBlockingConfig(cfg providerprofile.Config) providerprofile.Config {
	cfg.Capabilities.Streaming = openaiBoolPtr(false)
	return cfg
}

func openaiCodexCompletedTextResponse(r *http.Request, text string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader(
			fmt.Sprintf(`data: {"type":"response.completed","response":{"id":"resp_1","model":"m","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":%q,"annotations":[]}]}]}}`+"\n\n", text),
		)),
		Request: r,
	}
}

func openaiCodexCompletedWebsocketEvent(id, text string) []byte {
	event := map[string]any{
		"type": "response.completed",
		"response": map[string]any{
			"id":     id,
			"model":  "gpt-test",
			"status": "completed",
			"output": []any{
				map[string]any{
					"type":   "message",
					"id":     "msg_" + id,
					"role":   "assistant",
					"status": "completed",
					"content": []any{
						map[string]any{"type": "output_text", "text": text, "annotations": []any{}},
					},
				},
			},
			"usage": map[string]any{"input_tokens": 1, "output_tokens": 1, "total_tokens": 2},
		},
	}
	raw, _ := json.Marshal(event)
	return raw
}

type openaiErrReadCloser struct {
	err error
}

type openaiIdleReadCloser struct {
	ctx    context.Context
	prefix *strings.Reader
}

func openaiIsWebsocketRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func openaiRequireStreamIdleTimeout(t testing.TB, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("err = %v, want stream idle timeout", err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, must not be context.Canceled", err)
	}
}

type openaiRoundTripFunc func(*http.Request) (*http.Response, error)

func openaiTestBlockingProfile(t testing.TB, cfg providerprofile.Config) llm.ProviderProfile {
	t.Helper()
	return openaiTestProfile(t, openaiBlockingConfig(cfg))
}

func openaiTestImageMedia(t *testing.T) (*llm.MediaRef, string) {
	t.Helper()
	dir := t.TempDir()
	data := []byte("fake image bytes")
	store, err := artifact.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("threads/123456/media/image.png", data)
	if err != nil {
		t.Fatal(err)
	}
	return &llm.MediaRef{
		ArtifactPath:  ref.Path,
		MediaType:     "image/png",
		SHA256:        ref.SHA256,
		OriginalBytes: len(data),
		Width:         2,
		Height:        1,
	}, dir
}

func openaiTestProfile(t testing.TB, cfg providerprofile.Config) llm.ProviderProfile {
	t.Helper()
	profile, err := providerprofile.ResolveProfile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func (f openaiRoundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func (r openaiErrReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r openaiErrReadCloser) Close() error {
	return nil
}

func (r *openaiIdleReadCloser) Read(p []byte) (int, error) {
	if r.prefix != nil && r.prefix.Len() > 0 {
		return r.prefix.Read(p)
	}
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}

func (r *openaiIdleReadCloser) Close() error {
	return nil
}

func openaiBoolPtr(v bool) *bool {
	return &v
}

func newTestProvider(cfg providerprofile.Config) (llm.Provider, error) {
	resolved, err := providerprofile.ResolveProfile(cfg)
	if err != nil {
		return nil, err
	}
	switch resolved.Protocol {
	case llm.ProtocolOpenAIChat:
		return NewOpenAI(resolved, nil), nil
	case llm.ProtocolOpenAIResponses:
		return NewOpenAIResponses(resolved, nil), nil
	case llm.ProtocolOpenAICodexResponses:
		return NewOpenAICodexResponses(resolved, nil), nil
	default:
		return nil, fmt.Errorf("unexpected test protocol %s", resolved.Protocol)
	}
}
