package anthropic

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
	"testing"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/juex-ai/juex/internal/foundation/artifact"
	"github.com/juex-ai/juex/internal/foundation/llm"
	providerprofile "github.com/juex-ai/juex/internal/providers/profile"
)

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
		anthropicWriteAnthropicTextAndToolStream(w, "claude-test", "hi there", 10, 5)
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)

	resp, err := p.Complete(context.Background(), "system text",
		[]llm.Message{llm.TextMessage(llm.RoleUser, "hello")},
		[]llm.ToolSpec{{Name: "read", Description: "read a file", Schema: map[string]any{
			"type":       "object",
			"properties": map[string]any{"path": map[string]any{"type": "string"}},
			"required":   []string{"path"},
		}}},
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.StopReason != llm.StopToolUse {
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

func TestAnthropic_ProjectsUserAndToolResultImages(t *testing.T) {
	media, mediaDir := anthropicTestImageMedia(t)
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		anthropicWriteAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 2)
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{
		ID:           "anthropic",
		BaseURL:      srv.URL,
		APIKey:       "test-key",
		Model:        "claude-test",
		MediaDir:     mediaDir,
		Capabilities: llm.CapabilityOverrides{Vision: anthropicBoolPtr(true)},
	}), nil)

	hist := []llm.Message{
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockText, Text: "look"}, {Type: llm.BlockImage, Media: media}}},
		{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "call_1", ToolName: "render_chart", Input: map[string]any{"id": "x"}}}},
		{Role: llm.RoleUser, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call_1", ToolName: "render_chart", Content: "chart", Media: media}}},
	}
	if _, err := p.Complete(context.Background(), "", hist, []llm.ToolSpec{{Name: "render_chart", Schema: map[string]any{"type": "object"}}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	messages, _ := capturedBody["messages"].([]any)
	if len(messages) != 3 {
		t.Fatalf("messages = %+v", messages)
	}
	firstContent, _ := messages[0].(map[string]any)["content"].([]any)
	if len(firstContent) != 2 || firstContent[1].(map[string]any)["type"] != "image" {
		t.Fatalf("user image content = %+v", firstContent)
	}
	source, _ := firstContent[1].(map[string]any)["source"].(map[string]any)
	if source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] == "" {
		t.Fatalf("anthropic image source = %+v", source)
	}
	toolContent, _ := messages[2].(map[string]any)["content"].([]any)
	if len(toolContent) != 1 || toolContent[0].(map[string]any)["type"] != "tool_result" {
		t.Fatalf("tool result wrapper = %+v", toolContent)
	}
	toolInner, _ := toolContent[0].(map[string]any)["content"].([]any)
	if len(toolInner) != 2 || toolInner[1].(map[string]any)["type"] != "image" {
		t.Fatalf("tool result image content = %+v", toolInner)
	}
	if strings.Contains(fmt.Sprint(messages), "cannot view image content") {
		t.Fatalf("vision-capable anthropic request contains unavailable-image guidance: %+v", messages)
	}
}

func TestAnthropic_MalformedStreamChunkReturnsDiagnosticError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-test","content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`+"\n\n")
		fmt.Fprint(w, "event: content_block_start\n")
		fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":`+"\n\n")
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)

	_, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err == nil {
		t.Fatal("Complete succeeded, want diagnostic stream parse error")
	}
	var streamErr *llm.StreamParseError
	if !errors.As(err, &streamErr) {
		t.Fatalf("error type = %T, want StreamParseError: %v", err, err)
	}
	if streamErr.Kind != llm.StreamParseErrorKindAnthropic || streamErr.EventType != "content_block_start" {
		t.Fatalf("stream error = %+v", streamErr)
	}
	if !streamErr.HasIndex || streamErr.Index != 0 {
		t.Fatalf("stream error index = %+v, want index 0", streamErr)
	}
	if streamErr.Cause == nil || !strings.Contains(streamErr.Cause.Error(), "unexpected end of JSON input") {
		t.Fatalf("stream error cause = %v", streamErr.Cause)
	}
	for _, want := range []string{
		"kind=anthropic_stream_parse",
		"provider=anthropic:claude-test",
		"event_type=content_block_start",
		"index=0",
		"raw_preview=",
		"unexpected end of JSON input",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err.Error(), want)
		}
	}
}

func TestAnthropic_SplitInputJSONDeltaStillAccumulates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicWriteAnthropicSplitToolInputStream(w, "claude-test")
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)

	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls = %+v, want one", calls)
	}
	if calls[0].Input["path"] != "/tmp/x" {
		t.Fatalf("tool input = %+v, want path /tmp/x", calls[0].Input)
	}
}

func TestAnthropic_ToolWithoutPropertiesUsesEmptyObject(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		anthropicWriteAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 5)
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)

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
	inputSchema, _ := tool["input_schema"].(map[string]any)
	anthropicAssertEmptyProperties(t, inputSchema)
}

func TestAnthropic_CompleteOptionsAddsCacheControlAndRecordsCacheReadTokens(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		anthropicWriteAnthropicTextStreamWithCache(w, "claude-test", "ok", "end_turn", 100, 5, 64)
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeContext := llm.TextMessage(llm.RoleUser, "runtime state changes every turn")
	runtimeContext.Kind = llm.MessageKindRuntimeContext
	resp, err := llm.CompleteWithOptions(context.Background(), p, "system text", []llm.Message{
		llm.TextMessage(llm.RoleUser, "hello"),
		runtimeContext,
	}, []llm.ToolSpec{
		{Name: "read", Description: "read a file", Schema: map[string]any{"type": "object"}},
	}, llm.CompleteOptions{CachePolicy: llm.CachePolicy{StablePrefixKey: "juex-cache-key", Retention: "1h"}})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}

	sysBlocks, _ := capturedBody["system"].([]any)
	if len(sysBlocks) != 1 {
		t.Fatalf("system = %+v", capturedBody["system"])
	}
	sysBlock, _ := sysBlocks[0].(map[string]any)
	cacheControl, _ := sysBlock["cache_control"].(map[string]any)
	if cacheControl["type"] != "ephemeral" || cacheControl["ttl"] != "1h" {
		t.Fatalf("system cache_control = %+v", cacheControl)
	}
	tools, _ := capturedBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %+v", capturedBody["tools"])
	}
	tool, _ := tools[0].(map[string]any)
	cacheControl, _ = tool["cache_control"].(map[string]any)
	if cacheControl["type"] != "ephemeral" || cacheControl["ttl"] != "1h" {
		t.Fatalf("tool cache_control = %+v", cacheControl)
	}
	msgs, _ := capturedBody["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %+v", capturedBody["messages"])
	}
	durableContent, _ := msgs[0].(map[string]any)["content"].([]any)
	if len(durableContent) != 1 {
		t.Fatalf("durable message content = %+v", msgs[0])
	}
	durableBlock, _ := durableContent[0].(map[string]any)
	cacheControl, _ = durableBlock["cache_control"].(map[string]any)
	if cacheControl["type"] != "ephemeral" || cacheControl["ttl"] != "1h" {
		t.Fatalf("history cache_control should be on durable history block, got %+v", durableBlock)
	}
	runtimeContent, _ := msgs[1].(map[string]any)["content"].([]any)
	if len(runtimeContent) != 1 {
		t.Fatalf("runtime message content = %+v", msgs[1])
	}
	runtimeBlock, _ := runtimeContent[0].(map[string]any)
	if _, ok := runtimeBlock["cache_control"]; ok {
		t.Fatalf("runtime context must not carry history cache_control: %+v", runtimeBlock)
	}
	if resp.Usage.CachedInputTokens != 64 {
		t.Fatalf("cached input tokens = %d", resp.Usage.CachedInputTokens)
	}
}

func TestAnthropic_UsesMessageDeltaInputUsageWhenMessageStartIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		anthropicWriteAnthropicTextStreamWithDeltaUsage(w, "claude-test", 0, 0, 178, 50, 7)
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)
	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 235 || resp.Usage.CachedInputTokens != 50 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want input=235 cached=50 output=5", resp.Usage)
	}
}

func TestAnthropic_UsesMessageDeltaCacheUsageWhenInputIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		anthropicWriteAnthropicTextStreamWithDeltaUsage(w, "claude-test", 0, 0, 0, 50, 7)
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)
	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 57 || resp.Usage.CachedInputTokens != 50 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want input=57 cached=50 output=5", resp.Usage)
	}
}

func TestAnthropic_MessageStartInputUsageTakesPrecedenceOverDelta(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		anthropicWriteAnthropicTextStreamWithDeltaUsage(w, "claude-test", 1234, 64, 178, 50, 7)
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)
	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 1298 || resp.Usage.CachedInputTokens != 64 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want official start usage input=1298 cached=64 output=5", resp.Usage)
	}
}

func TestAnthropic_MessageStartCacheUsageTakesPrecedenceWhenInputIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		anthropicWriteAnthropicTextStreamWithDeltaUsage(w, "claude-test", 0, 64, 0, 50, 7)
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)
	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Usage.InputTokens != 71 || resp.Usage.CachedInputTokens != 64 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want official start usage input=71 cached=64 output=5", resp.Usage)
	}
}

func TestAnthropicStreamUsageObservationUsesLastNonzeroDeltaFields(t *testing.T) {
	var observed anthropicStreamUsageObservation
	observed.observe(anthropic.MessageStreamEventUnion{
		Type: "message_delta",
		Usage: anthropic.MessageDeltaUsage{
			InputTokens:              178,
			CacheReadInputTokens:     50,
			CacheCreationInputTokens: 7,
		},
	})
	observed.observe(anthropic.MessageStreamEventUnion{
		Type: "message_delta",
		Usage: anthropic.MessageDeltaUsage{
			CacheReadInputTokens: 51,
		},
	})
	msg := anthropic.Message{}

	observed.applyFallback(&msg)

	if msg.Usage.InputTokens != 178 || msg.Usage.CacheReadInputTokens != 51 || msg.Usage.CacheCreationInputTokens != 7 {
		t.Fatalf("usage = %+v, want last nonzero delta fields", msg.Usage)
	}
}

func TestAnthropic_CompleteOptionsEmitsStreamDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicWriteAnthropicThinkingAndTextStream(w, "claude-test")
	}))
	defer srv.Close()

	p, err := newTestProvider(providerprofile.Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test"})
	if err != nil {
		t.Fatal(err)
	}
	withOpts := p.(llm.ProviderWithOptions)
	var deltas []llm.StreamDelta
	resp, err := withOpts.CompleteWithOptions(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hello")}, nil, llm.CompleteOptions{
		OnDelta: func(delta llm.StreamDelta) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if resp.Message.FirstText() != "hello stream" {
		t.Fatalf("text = %q", resp.Message.FirstText())
	}
	want := []llm.StreamDelta{
		{Kind: "reasoning", Index: 0, Text: "plan "},
		{Kind: "reasoning", Index: 0, Text: "then "},
		{Kind: "text", Index: 1, Text: "hello "},
		{Kind: "text", Index: 1, Text: "stream"},
	}
	if !slices.Equal(deltas, want) {
		t.Fatalf("deltas = %+v, want %+v", deltas, want)
	}
}

func TestAnthropic_CompactsEmptyHistoryMessages(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		anthropicWriteAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 1)
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test"}), nil)
	hist := []llm.Message{
		llm.TextMessage(llm.RoleUser, "hello"),
		{Role: llm.RoleAssistant, Blocks: []llm.Block{}},
		{Role: llm.RoleAssistant, Blocks: nil},
		llm.TextMessage(llm.RoleUser, "again"),
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

func TestAnthropic_ThinkingEffort(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		anthropicWriteAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 1)
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test", ThinkingEffort: "max"}), nil)
	if _, err := llm.CompleteWithOptions(context.Background(), p, "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{ThinkingEffort: "low"}); err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	outputConfig, ok := capturedBody["output_config"].(map[string]any)
	if !ok {
		t.Fatalf("output_config not present or wrong type: %+v", capturedBody["output_config"])
	}
	if outputConfig["effort"] != "low" {
		t.Errorf("output_config.effort = %v, want %q", outputConfig["effort"], "low")
	}
	thinking, ok := capturedBody["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("thinking not present or wrong type: %+v", capturedBody["thinking"])
	}
	if thinking["type"] != "adaptive" {
		t.Errorf("thinking.type = %v, want %q", thinking["type"], "adaptive")
	}
	if thinking["display"] != "summarized" {
		t.Errorf("thinking.display = %v, want %q", thinking["display"], "summarized")
	}
	if _, present := thinking["budget_tokens"]; present {
		t.Fatalf("thinking should not use deprecated budget_tokens: %+v", thinking)
	}
	if capturedBody["max_tokens"] != float64(4096) {
		t.Errorf("max_tokens = %v, want provider visible-output default 4096", capturedBody["max_tokens"])
	}
}

func TestAnthropic_DefaultEffortUsesAdaptiveThinking(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		anthropicWriteAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 1)
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test"}), nil)
	if _, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, present := capturedBody["output_config"]; present {
		t.Errorf("output_config should be absent when effort is empty, got %v", capturedBody["output_config"])
	}
	thinking, ok := capturedBody["thinking"].(map[string]any)
	if !ok || thinking["type"] != "adaptive" {
		t.Fatalf("thinking = %+v, want adaptive", capturedBody["thinking"])
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

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "minimax-m2.7"}), nil)
	resp, err := p.Complete(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil)
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
		anthropicWriteAnthropicTextStream(w, "claude-test", "summary", "end_turn", 1, 2)
	}))
	defer srv.Close()

	p := NewAnthropic(anthropicTestProfile(t, providerprofile.Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "minimax-m2.7", ThinkingEffort: "high"}), nil)
	withOpts, ok := p.(llm.ProviderWithOptions)
	if !ok {
		t.Fatal("anthropic provider does not implement ProviderWithOptions")
	}
	_, err := withOpts.CompleteWithOptions(context.Background(), "", []llm.Message{llm.TextMessage(llm.RoleUser, "hi")}, nil, llm.CompleteOptions{
		Purpose:         "compaction",
		MaxOutputTokens: 1234,
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	outputConfig, ok := captured["output_config"].(map[string]any)
	if !ok || outputConfig["effort"] != "high" {
		t.Fatalf("output_config = %+v, want high effort", captured["output_config"])
	}
	thinking, ok := captured["thinking"].(map[string]any)
	if !ok || thinking["type"] != "adaptive" {
		t.Fatalf("thinking = %+v, want adaptive", captured["thinking"])
	}
	if _, present := thinking["budget_tokens"]; present {
		t.Fatalf("thinking should not use deprecated budget_tokens: %+v", thinking)
	}
	if captured["max_tokens"] != float64(1234) {
		t.Fatalf("max_tokens = %v, want visible output token cap", captured["max_tokens"])
	}
}

func anthropicAssertEmptyProperties(t *testing.T, schema map[string]any) {
	t.Helper()
	anthropicAssertObjectWithEmptyProperties(t, schema)
	if schema["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %+v, want false in empty object schema %+v", schema["additionalProperties"], schema)
	}
}

func anthropicAssertObjectWithEmptyProperties(t *testing.T, schema map[string]any) {
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

func anthropicTestImageMedia(t *testing.T) (*llm.MediaRef, string) {
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

func anthropicTestProfile(t testing.TB, cfg providerprofile.Config) llm.ProviderProfile {
	t.Helper()
	profile, err := providerprofile.ResolveProfile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func anthropicWriteAnthropicSplitToolInputStream(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: message_start\n")
	fmt.Fprintf(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`+"\n\n", model)
	fmt.Fprint(w, "event: content_block_start\n")
	fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"read","input":{}}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"/tmp/x\"}"}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_stop\n")
	fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
	fmt.Fprint(w, "event: message_delta\n")
	fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":4}}`+"\n\n")
	fmt.Fprint(w, "event: message_stop\n")
	fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
}

func anthropicWriteAnthropicTextAndToolStream(w http.ResponseWriter, model, text string, inputTokens, outputTokens int) {
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

func anthropicWriteAnthropicTextStream(w http.ResponseWriter, model, text, stopReason string, inputTokens, outputTokens int) {
	anthropicWriteAnthropicTextStreamWithCache(w, model, text, stopReason, inputTokens, outputTokens, 0)
}

func anthropicWriteAnthropicTextStreamWithCache(w http.ResponseWriter, model, text, stopReason string, inputTokens, outputTokens, cacheReadTokens int) {
	if stopReason == "" {
		stopReason = "end_turn"
	}
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: message_start\n")
	fmt.Fprintf(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"usage":{"input_tokens":%d,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":%d}}}`+"\n\n", model, inputTokens, cacheReadTokens)
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

func anthropicWriteAnthropicTextStreamWithDeltaUsage(
	w http.ResponseWriter,
	model string,
	startInputTokens, startCacheReadTokens int,
	deltaInputTokens, deltaCacheReadTokens, deltaCacheCreationTokens int,
) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: message_start\n")
	fmt.Fprintf(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"usage":{"input_tokens":%d,"output_tokens":0,"cache_creation_input_tokens":0,"cache_read_input_tokens":%d}}}`+"\n\n", model, startInputTokens, startCacheReadTokens)
	fmt.Fprint(w, "event: content_block_start\n")
	fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_stop\n")
	fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
	fmt.Fprint(w, "event: message_delta\n")
	fmt.Fprintf(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":%d,"output_tokens":5,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}`+"\n\n", deltaInputTokens, deltaCacheCreationTokens, deltaCacheReadTokens)
	fmt.Fprint(w, "event: message_stop\n")
	fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
}

func anthropicWriteAnthropicThinkingAndTextStream(w http.ResponseWriter, model string) {
	w.Header().Set("Content-Type", "text/event-stream")
	fmt.Fprint(w, "event: message_start\n")
	fmt.Fprintf(w, `data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":%q,"content":[],"stop_reason":null,"usage":{"input_tokens":10,"output_tokens":0}}}`+"\n\n", model)
	fmt.Fprint(w, "event: content_block_start\n")
	fmt.Fprint(w, `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"plan "}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"then "}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprint(w, `data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_1"}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_stop\n")
	fmt.Fprint(w, `data: {"type":"content_block_stop","index":0}`+"\n\n")
	fmt.Fprint(w, "event: content_block_start\n")
	fmt.Fprint(w, `data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprint(w, `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"hello "}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_delta\n")
	fmt.Fprint(w, `data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"stream"}}`+"\n\n")
	fmt.Fprint(w, "event: content_block_stop\n")
	fmt.Fprint(w, `data: {"type":"content_block_stop","index":1}`+"\n\n")
	fmt.Fprint(w, "event: message_delta\n")
	fmt.Fprint(w, `data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`+"\n\n")
	fmt.Fprint(w, "event: message_stop\n")
	fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
}

func anthropicBoolPtr(v bool) *bool {
	return &v
}

func newTestProvider(cfg providerprofile.Config) (llm.Provider, error) {
	resolved, err := providerprofile.ResolveProfile(cfg)
	if err != nil {
		return nil, err
	}
	return NewAnthropic(resolved, nil), nil
}
