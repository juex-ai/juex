package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The SDK clients accept option.WithBaseURL, so we point them at httptest
// servers and assert on the wire payload + the way the canonical types come
// back. This is end-to-end coverage of the provider adapter, but without
// hitting the real Anthropic / OpenAI APIs.

func writeAnthropicTextStream(w http.ResponseWriter, model, text, stopReason string, inputTokens, outputTokens int) {
	if stopReason == "" {
		stopReason = "end_turn"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: message_start\n")
	fmt.Fprintf(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"usage":{"input_tokens":%d,"output_tokens":0}}}`+"\n\n", model, inputTokens)
	fmt.Fprint(w, "event: content_block_start\n")
	fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprintf(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`+"\n\n", text)
	fmt.Fprint(w, "event: content_block_stop\n")
	fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
	fmt.Fprint(w, "event: message_delta\n")
	fmt.Fprintf(w, `data: {"type":"message_delta","delta":{"stop_reason":%q,"stop_sequence":null},"usage":{"output_tokens":%d}}`+"\n\n", stopReason, outputTokens)
	fmt.Fprint(w, "event: message_stop\n")
	fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
}

func writeAnthropicTextAndToolStream(w http.ResponseWriter, model, text string, inputTokens, outputTokens int) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: message_start\n")
	fmt.Fprintf(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"usage":{"input_tokens":%d,"output_tokens":0}}}`+"\n\n", model, inputTokens)
	fmt.Fprint(w, "event: content_block_start\n")
	fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprintf(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":%q}}`+"\n\n", text)
	fmt.Fprint(w, "event: content_block_stop\n")
	fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
	fmt.Fprint(w, "event: content_block_start\n")
	fmt.Fprint(w, `data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"read","input":{}}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprint(w, `data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/tmp/x\"}"}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_stop\n")
	fmt.Fprint(w, `data: {"type":"content_block_stop","index":1}`+"\n\n")
	fmt.Fprint(w, "event: message_delta\n")
	fmt.Fprintf(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":%d}}`+"\n\n", outputTokens)
	fmt.Fprint(w, "event: message_stop\n")
	fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
}

func anthropicTextStreamData(model, text, stopReason string, inputTokens, outputTokens int) string {
	var sb strings.Builder
	rr := httptest.NewRecorder()
	writeAnthropicTextStream(rr, model, text, stopReason, inputTokens, outputTokens)
	sb.WriteString(rr.Body.String())
	return sb.String()
}

func TestAnthropic_RoundTrip(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/v1/messages") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing api key header: %v", r.Header)
		}
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		writeAnthropicTextAndToolStream(w, "claude-test", "hi there", 10, 5)
	}))
	defer srv.Close()

	p := NewAnthropic(Config{
		Type:    "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}, nil)

	resp, err := p.Complete(context.Background(), "system text",
		[]Message{TextMessage(RoleUser, "hello")},
		[]ToolSpec{{Name: "read", Description: "read a file", Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		}}},
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop reason = %s", resp.StopReason)
	}
	if resp.Message.FirstText() != "hi there" {
		t.Errorf("text = %q", resp.Message.FirstText())
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || calls[0].ToolName != "read" {
		t.Errorf("tool calls = %+v", calls)
	}
	if calls[0].Input["path"] != "/tmp/x" {
		t.Errorf("tool input = %+v", calls[0].Input)
	}
	if capturedBody["model"] != "claude-test" {
		t.Errorf("model not propagated: %+v", capturedBody)
	}
	if capturedBody["stream"] != true {
		t.Errorf("anthropic provider should always request streaming: %+v", capturedBody["stream"])
	}
	// Anthropic SDK marshals system as an array of TextBlockParam.
	sysBlocks, ok := capturedBody["system"].([]any)
	if !ok || len(sysBlocks) == 0 {
		t.Errorf("system not propagated: %+v", capturedBody["system"])
	}
}

func TestAnthropic_CompactsEmptyHistoryMessages(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		writeAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 1)
	}))
	defer srv.Close()

	p := NewAnthropic(Config{Type: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test"}, nil)
	hist := []Message{
		TextMessage(RoleUser, "hello"),
		{Role: RoleAssistant, Blocks: []Block{}},
		{Role: RoleAssistant, Blocks: nil},
		TextMessage(RoleUser, "again"),
	}
	if _, err := p.Complete(context.Background(), "", hist, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs, ok := capturedBody["messages"].([]any)
	if !ok {
		t.Fatalf("messages missing: %+v", capturedBody)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages len = %d, want merged user message; body=%+v", len(msgs), capturedBody)
	}
	content, ok := msgs[0].(map[string]any)["content"].([]any)
	if !ok || len(content) != 2 {
		t.Fatalf("merged content = %+v, want two text blocks", msgs[0])
	}
}

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

	p := NewOpenAI(Config{
		Type:    "openai",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "gpt-test",
	}, nil)

	resp, err := p.Complete(context.Background(), "system text",
		[]Message{TextMessage(RoleUser, "hello")},
		[]ToolSpec{{Name: "grep", Description: "grep", Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"pattern": map[string]any{"type": "string"}},
			"required":   []string{"pattern"},
		}}},
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.StopReason != StopToolUse {
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

func TestProviders_RetryPolicy(t *testing.T) {
	cases := []struct {
		name               string
		provider           func(baseURL string) Provider
		serverErr          string
		badReqErr          string
		successContentType string
		successRes         string
	}{
		{
			name: "anthropic",
			provider: func(baseURL string) Provider {
				return NewAnthropic(Config{Type: "anthropic", BaseURL: baseURL, APIKey: "test-key", Model: "claude-test"}, nil)
			},
			serverErr:          `{"type":"error","error":{"type":"api_error","message":"temporary server error"}}`,
			badReqErr:          `{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`,
			successContentType: "text/event-stream",
			successRes:         anthropicTextStreamData("claude-test", "ok", "end_turn", 1, 1),
		},
		{
			name: "openai",
			provider: func(baseURL string) Provider {
				return NewOpenAI(Config{Type: "openai", BaseURL: baseURL, APIKey: "k", Model: "m"}, nil)
			},
			serverErr:          `{"error":{"message":"temporary server error","type":"server_error"}}`,
			badReqErr:          `{"error":{"message":"bad request","type":"invalid_request_error"}}`,
			successContentType: "application/json",
			successRes: `{
				"id":"cmpl_1","object":"chat.completion","model":"gpt-test",
				"choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
				"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
			}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name+"/recoverable", func(t *testing.T) {
			attempts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.Header().Set("retry-after-ms", "0")
				if attempts <= providerMaxRetries {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(tc.serverErr))
					return
				}
				w.Header().Set("Content-Type", tc.successContentType)
				w.Write([]byte(tc.successRes))
			}))
			defer srv.Close()

			resp, err := tc.provider(srv.URL).Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if attempts != providerMaxRetries+1 {
				t.Fatalf("attempts = %d, want %d", attempts, providerMaxRetries+1)
			}
			if resp.Message.FirstText() != "ok" {
				t.Fatalf("text = %q, want ok", resp.Message.FirstText())
			}
		})

		t.Run(tc.name+"/bad_request", func(t *testing.T) {
			attempts := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				attempts++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(tc.badReqErr))
			}))
			defer srv.Close()

			if _, err := tc.provider(srv.URL).Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil); err == nil {
				t.Fatal("expected error")
			}
			if attempts != 1 {
				t.Fatalf("attempts = %d, want 1", attempts)
			}
		})
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

	p := NewOpenAI(Config{Type: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"}, nil)
	hist := []Message{
		TextMessage(RoleUser, "hello"),
		{Role: RoleAssistant, Blocks: []Block{}},
		{Role: RoleAssistant, Blocks: nil},
		TextMessage(RoleUser, "again"),
		{Role: RoleSystem, Blocks: nil},
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

	p := NewOpenAI(Config{Type: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"}, nil)

	hist := []Message{
		TextMessage(RoleUser, "do it"),
		{Role: RoleAssistant, Blocks: []Block{
			{Type: BlockText, Text: "ok"},
			{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "read", Input: map[string]any{"path": "x"}},
		}},
		{Role: RoleUser, Blocks: []Block{
			{Type: BlockToolResult, ToolUseID: "call_1", Content: "file contents"},
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

func TestNewProvider_Errors(t *testing.T) {
	if _, err := New(Config{Type: "anthropic", APIKey: "", Model: "m"}); err == nil {
		t.Error("missing key should error")
	}
	if _, err := New(Config{Type: "anthropic", APIKey: "k"}); err == nil {
		t.Error("missing model should error")
	}
	if _, err := New(Config{Type: "bogus", APIKey: "k", Model: "m"}); err == nil {
		t.Error("unknown type should error")
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

	p := NewOpenAI(Config{Type: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m", ThinkingEffort: "low"}, nil)
	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if capturedBody["reasoning_effort"] != "low" {
		t.Errorf("reasoning_effort = %v, want %q", capturedBody["reasoning_effort"], "low")
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

	p := NewOpenAI(Config{Type: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"}, nil)
	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil); err != nil {
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

	p := NewOpenAI(Config{Type: "openai", BaseURL: srv.URL, APIKey: "k", Model: "m"}, nil)
	withOpts, ok := p.(ProviderWithOptions)
	if !ok {
		t.Fatal("openai provider does not implement ProviderWithOptions")
	}
	if _, err := withOpts.CompleteWithOptions(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
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
	p := NewOpenAI(Config{
		Type:           "openai",
		BaseURL:        srv.URL,
		APIKey:         "k",
		Model:          "m",
		ThinkingEffort: "low",
		Capabilities: CapabilityOverrides{
			Tools:           &disabled,
			ReasoningEffort: &disabled,
			ReasoningReplay: &disabled,
			MaxOutputTokens: &disabled,
		},
	}, nil)
	withOpts, ok := p.(ProviderWithOptions)
	if !ok {
		t.Fatal("openai provider does not implement ProviderWithOptions")
	}
	history := []Message{
		TextMessage(RoleUser, "do it"),
		{Role: RoleAssistant, Blocks: []Block{
			{Type: BlockReasoning, Text: "hidden"},
			{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "read", Input: map[string]any{"path": "x"}},
		}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "call_1", Content: "file"}}},
	}
	if _, err := withOpts.CompleteWithOptions(context.Background(), "", history, []ToolSpec{{Name: "read"}}, CompleteOptions{MaxOutputTokens: 123}); err != nil {
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
				{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hello","annotations":[]}]},
				{"type":"function_call","id":"fc_1","call_id":"call_1","name":"read","arguments":"{\"path\":\"x\"}","status":"completed"}
			],
			"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}
		}`))
	}))
	defer srv.Close()

	p, err := New(Config{
		ID:             "openai",
		Protocol:       "openai/responses",
		BaseURL:        srv.URL,
		APIKey:         "k",
		Model:          "gpt-test",
		ThinkingEffort: "low",
		Headers:        map[string]string{"X-Juex-Test": "yes"},
		Query:          map[string]string{"trace": "1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), "system",
		[]Message{
			TextMessage(RoleUser, "hello"),
			{Role: RoleAssistant, Blocks: []Block{
				{Type: BlockReasoning, Text: "prior", Signature: "rs_prev", Content: "enc_prev", Redacted: true},
				{Type: BlockToolUse, ToolUseID: "call_prev", ToolName: "read", Input: map[string]any{"path": "old"}},
			}},
			{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "call_prev", Content: "old file"}}},
		},
		[]ToolSpec{{Name: "read", Description: "read a file", Schema: map[string]any{"type": "object"}}},
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
	if capturedBody["reasoning"] == nil || capturedBody["include"] == nil {
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
	if resp.StopReason != StopToolUse {
		t.Fatalf("stop reason = %s, want tool_use", resp.StopReason)
	}
	if resp.Message.FirstText() != "hello" {
		t.Fatalf("text = %q", resp.Message.FirstText())
	}
	if calls := resp.Message.ToolCalls(); len(calls) != 1 || calls[0].ToolName != "read" || calls[0].Input["path"] != "x" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if len(resp.Message.Blocks) == 0 || resp.Message.Blocks[0].Type != BlockReasoning || resp.Message.Blocks[0].Signature != "rs_1" {
		t.Fatalf("reasoning block not preserved: %+v", resp.Message.Blocks)
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
}

func TestAnthropic_ThinkingEffort(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		writeAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 1)
	}))
	defer srv.Close()

	p := NewAnthropic(Config{Type: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test", ThinkingEffort: "low"}, nil)
	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	thinking, ok := capturedBody["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking not present or wrong type: %+v", capturedBody["thinking"])
	}
	if thinking["type"] != "enabled" {
		t.Errorf("thinking.type = %v, want %q", thinking["type"], "enabled")
	}
	budgetTokens, _ := thinking["budget_tokens"].(float64)
	if budgetTokens != 2048 {
		t.Errorf("thinking.budget_tokens = %v, want 2048", budgetTokens)
	}
	// max_tokens should be bumped when thinking is enabled
	maxTokens, _ := capturedBody["max_tokens"].(float64)
	if maxTokens != 8192 {
		t.Errorf("max_tokens = %v, want 8192", maxTokens)
	}
}

func TestAnthropic_NoThinkingEffort(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		writeAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 1)
	}))
	defer srv.Close()

	p := NewAnthropic(Config{Type: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test"}, nil)
	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, present := capturedBody["thinking"]; present {
		t.Errorf("thinking should be absent when not configured, got %v", capturedBody["thinking"])
	}
	maxTokens, _ := capturedBody["max_tokens"].(float64)
	if maxTokens != 4096 {
		t.Errorf("max_tokens = %v, want 4096", maxTokens)
	}
}

func TestAnthropic_AlwaysUsesStreaming(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":11,"output_tokens":1}}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_delta\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"streamed ok"}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_stop\n")
		fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
		fmt.Fprint(w, "event: message_delta\n")
		fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}`+"\n\n")
		fmt.Fprint(w, "event: message_stop\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer srv.Close()

	p := NewAnthropic(Config{Type: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "minimax-m2.7"}, nil)
	resp, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Message.FirstText() != "streamed ok" {
		t.Fatalf("text = %q", resp.Message.FirstText())
	}
	if resp.Usage.InputTokens != 11 || resp.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	if captured["stream"] != true {
		t.Fatalf("stream flag = %v, want true", captured["stream"])
	}
}

func TestProviderCompleteOptions_PreservesThinkingForCompaction(t *testing.T) {
	var captured map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &captured); err != nil {
			t.Fatal(err)
		}
		writeAnthropicTextStream(w, "claude-test", "summary", "end_turn", 1, 2)
	}))
	defer srv.Close()

	p := NewAnthropic(Config{Type: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "minimax-m2.7", ThinkingEffort: "high"}, nil)
	withOpts, ok := p.(ProviderWithOptions)
	if !ok {
		t.Fatal("anthropic provider does not implement ProviderWithOptions")
	}
	_, err := withOpts.CompleteWithOptions(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		Purpose:         "compaction",
		MaxOutputTokens: 1234,
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	thinking, ok := captured["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking not preserved for compaction: %+v", captured["thinking"])
	}
	if thinking["type"] != "enabled" || thinking["budget_tokens"] != float64(32768) {
		t.Fatalf("thinking = %+v, want high budget", thinking)
	}
	if captured["max_tokens"] != float64(32768+1234) {
		t.Fatalf("max_tokens = %v, want thinking budget plus visible output", captured["max_tokens"])
	}
}

func TestIsContextOverflowError(t *testing.T) {
	for _, msg := range []string{
		"openai: context_length_exceeded",
		"maximum context length is 6400 tokens",
		"prompt is too long",
		"input length exceeds context window",
	} {
		if !IsContextOverflowError(fmt.Errorf("wrapped: %s", msg)) {
			t.Fatalf("expected overflow for %q", msg)
		}
	}
	if IsContextOverflowError(fmt.Errorf("rate limit exceeded")) {
		t.Fatal("rate limit should not be classified as context overflow")
	}
}
