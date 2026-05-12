package llm

import (
	"context"
	"encoding/json"
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
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"msg_1","type":"message","role":"assistant","model":"claude-test",
			"content":[
				{"type":"text","text":"hi there"},
				{"type":"tool_use","id":"tu_1","name":"read","input":{"path":"/tmp/x"}}
			],
			"stop_reason":"tool_use",
			"usage":{"input_tokens":10,"output_tokens":5}
		}`))
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
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"msg_1","type":"message","role":"assistant","model":"claude-test",
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":1}
		}`))
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

func TestAnthropic_ThinkingEffort(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"msg_1","type":"message","role":"assistant","model":"claude-test",
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":1}
		}`))
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
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"msg_1","type":"message","role":"assistant","model":"claude-test",
			"content":[{"type":"text","text":"ok"}],
			"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":1}
		}`))
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
