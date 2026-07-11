package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/juex-ai/juex/internal/artifact"
	"github.com/juex-ai/juex/internal/chunkedwrite"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type errReadCloser struct {
	err error
}

func (r errReadCloser) Read([]byte) (int, error) {
	return 0, r.err
}

func (r errReadCloser) Close() error {
	return nil
}

func testProfile(t testing.TB, cfg Config) ProviderProfile {
	t.Helper()
	profile, err := ResolveProfile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func testBlockingProfile(t testing.TB, cfg Config) ProviderProfile {
	t.Helper()
	return testProfile(t, blockingConfig(cfg))
}

func blockingConfig(cfg Config) Config {
	cfg.Capabilities.Streaming = boolPtr(false)
	return cfg
}

// The SDK clients accept option.WithBaseURL, so we point them at httptest
// servers and assert on the wire payload + the way the canonical types come
// back. This is end-to-end coverage of the provider adapter, but without
// hitting the real Anthropic / OpenAI APIs.

func writeAnthropicTextStream(w http.ResponseWriter, model, text, stopReason string, inputTokens, outputTokens int) {
	writeAnthropicTextStreamWithCache(w, model, text, stopReason, inputTokens, outputTokens, 0)
}

func writeAnthropicTextStreamWithCache(w http.ResponseWriter, model, text, stopReason string, inputTokens, outputTokens, cacheReadTokens int) {
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

func writeAnthropicThinkingAndTextStream(w http.ResponseWriter, model string) {
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

func testImageMedia(t *testing.T) *MediaRef {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	data := []byte("fake image bytes")
	store, err := artifact.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("media/session/image.png", data)
	if err != nil {
		t.Fatal(err)
	}
	return &MediaRef{
		ArtifactPath:  ref.Path,
		MediaType:     "image/png",
		SHA256:        ref.SHA256,
		OriginalBytes: len(data),
		Width:         2,
		Height:        1,
	}
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

func writeAnthropicSplitToolInputStream(w http.ResponseWriter, model string) {
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

	p := NewAnthropic(testProfile(t, Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)

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

func TestBlockImageJSONRoundTripStoresMediaReference(t *testing.T) {
	original := Message{
		Role: RoleUser,
		Blocks: []Block{{
			Type: BlockImage,
			Media: &MediaRef{
				ArtifactPath:  ".juex/artifacts/media/s/sha.png",
				MediaType:     "image/png",
				SHA256:        "sha",
				OriginalBytes: 123,
				Width:         8,
				Height:        9,
			},
		}},
	}
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "base64") {
		t.Fatalf("message JSON should store media references only: %s", data)
	}
	var roundTrip Message
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if len(roundTrip.Blocks) != 1 || roundTrip.Blocks[0].Type != BlockImage || roundTrip.Blocks[0].Media == nil {
		t.Fatalf("round trip = %+v", roundTrip)
	}
	if roundTrip.Blocks[0].Media.SHA256 != "sha" || roundTrip.Blocks[0].Media.Width != 8 {
		t.Fatalf("media ref = %+v", roundTrip.Blocks[0].Media)
	}
}

func TestReadImageBase64RejectsUnsafePathsAndMediaTypes(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile("image.txt", []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if encoded, mediaType, ok := readImageBase64("", &MediaRef{ArtifactPath: "image.txt", MediaType: "text/plain"}); ok || encoded != "" || mediaType != "" {
		t.Fatalf("text media should be rejected: encoded=%q mediaType=%q ok=%t", encoded, mediaType, ok)
	}
	for _, unsafe := range []string{"/etc/passwd", "../image.png", filepath.Join("..", "image.png")} {
		if encoded, mediaType, ok := readImageBase64("", &MediaRef{ArtifactPath: unsafe, MediaType: "image/png"}); ok || encoded != "" || mediaType != "" {
			t.Fatalf("unsafe path %q should be rejected: encoded=%q mediaType=%q ok=%t", unsafe, encoded, mediaType, ok)
		}
	}
}

func TestReadImageBase64ResolvesRelativeArtifactFromWorkDir(t *testing.T) {
	workDir := t.TempDir()
	otherDir := t.TempDir()
	artifactPath := ".juex/artifacts/media/session/image.png"
	filePath := filepath.Join(workDir, filepath.FromSlash(artifactPath))
	if err := os.MkdirAll(filepath.Dir(filePath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filePath, []byte("fake image bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(otherDir)

	encoded, mediaType, ok := readImageBase64(workDir, &MediaRef{ArtifactPath: artifactPath, MediaType: "image/png"})
	if !ok || mediaType != "image/png" || encoded == "" {
		t.Fatalf("readImageBase64 = encoded:%q mediaType:%q ok:%t", encoded, mediaType, ok)
	}
}

func TestReadImageBase64RejectsIntegrityMismatch(t *testing.T) {
	workDir := t.TempDir()
	store, err := artifact.NewStore(workDir)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := store.Put("media/session/image.png", []byte("fake image bytes"))
	if err != nil {
		t.Fatal(err)
	}
	if encoded, mediaType, ok := readImageBase64(workDir, &MediaRef{
		ArtifactPath: ref.Path,
		MediaType:    "image/png",
		SHA256:       strings.Repeat("0", 64),
	}); ok || encoded != "" || mediaType != "" {
		t.Fatalf("integrity mismatch accepted: encoded=%q mediaType=%q ok=%t", encoded, mediaType, ok)
	}
}

func TestAnthropic_ProjectsUserAndToolResultImages(t *testing.T) {
	media := testImageMedia(t)
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		writeAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 2)
	}))
	defer srv.Close()

	p := NewAnthropic(testProfile(t, Config{
		ID:           "anthropic",
		BaseURL:      srv.URL,
		APIKey:       "test-key",
		Model:        "claude-test",
		Capabilities: CapabilityOverrides{Vision: boolPtr(true)},
	}), nil)

	hist := []Message{
		{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "look"}, {Type: BlockImage, Media: media}}},
		{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "render_chart", Input: map[string]any{"id": "x"}}}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "call_1", ToolName: "render_chart", Content: "chart", Media: media}}},
	}
	if _, err := p.Complete(context.Background(), "", hist, []ToolSpec{{Name: "render_chart", Schema: map[string]any{"type": "object"}}}); err != nil {
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

	p := NewAnthropic(testProfile(t, Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)

	_, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
	if err == nil {
		t.Fatal("Complete succeeded, want diagnostic stream parse error")
	}
	var streamErr *StreamParseError
	if !errors.As(err, &streamErr) {
		t.Fatalf("error type = %T, want StreamParseError: %v", err, err)
	}
	if streamErr.Kind != StreamParseErrorKindAnthropic || streamErr.EventType != "content_block_start" {
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
		writeAnthropicSplitToolInputStream(w, "claude-test")
	}))
	defer srv.Close()

	p := NewAnthropic(testProfile(t, Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)

	resp, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
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
		writeAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 5)
	}))
	defer srv.Close()

	p := NewAnthropic(testProfile(t, Config{
		ID:      "anthropic",
		BaseURL: srv.URL,
		APIKey:  "test-key",
		Model:   "claude-test",
	}), nil)

	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hello")}, []ToolSpec{
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
	assertEmptyProperties(t, inputSchema)
}

func TestAnthropic_CompleteOptionsAddsCacheControlAndRecordsCacheReadTokens(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		writeAnthropicTextStreamWithCache(w, "claude-test", "ok", "end_turn", 100, 5, 64)
	}))
	defer srv.Close()

	p, err := New(Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeContext := TextMessage(RoleUser, "runtime state changes every turn")
	runtimeContext.Kind = MessageKindRuntimeContext
	resp, err := CompleteWithOptions(context.Background(), p, "system text", []Message{
		TextMessage(RoleUser, "hello"),
		runtimeContext,
	}, []ToolSpec{
		{Name: "read", Description: "read a file", Schema: map[string]any{"type": "object"}},
	}, CompleteOptions{CachePolicy: CachePolicy{StablePrefixKey: "juex-cache-key", Retention: "1h"}})
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

func TestProviderContextPreservesRuntimeContextKindBoundary(t *testing.T) {
	runtimeContext := TextMessage(RoleUser, "Current working observations")
	runtimeContext.Kind = MessageKindRuntimeContext
	ctx, err := BuildProviderContext([]Message{
		TextMessage(RoleUser, "durable user input"),
		runtimeContext,
	}, presetProfile("anthropic"), ProviderContextOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx.Messages) != 2 {
		t.Fatalf("messages len = %d, want durable and runtime context split: %+v", len(ctx.Messages), ctx.Messages)
	}
	if ctx.Messages[1].Kind != MessageKindRuntimeContext {
		t.Fatalf("runtime context kind = %q, want %q", ctx.Messages[1].Kind, MessageKindRuntimeContext)
	}
}

func TestAnthropic_CompleteOptionsEmitsStreamDeltas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeAnthropicThinkingAndTextStream(w, "claude-test")
	}))
	defer srv.Close()

	p, err := New(Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test"})
	if err != nil {
		t.Fatal(err)
	}
	withOpts := p.(ProviderWithOptions)
	var deltas []StreamDelta
	resp, err := withOpts.CompleteWithOptions(context.Background(), "", []Message{TextMessage(RoleUser, "hello")}, nil, CompleteOptions{
		OnDelta: func(delta StreamDelta) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if resp.Message.FirstText() != "hello stream" {
		t.Fatalf("text = %q", resp.Message.FirstText())
	}
	want := []StreamDelta{
		{Kind: "reasoning", Index: 0, Text: "plan "},
		{Kind: "reasoning", Index: 0, Text: "then "},
		{Kind: "text", Index: 1, Text: "hello "},
		{Kind: "text", Index: 1, Text: "stream"},
	}
	if !slices.Equal(deltas, want) {
		t.Fatalf("deltas = %+v, want %+v", deltas, want)
	}
}

func TestStreamIdleContextCancelsAfterTimeout(t *testing.T) {
	ctx, _, stop, expired := newStreamIdleContext(context.Background(), 10*time.Millisecond)
	defer stop()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream idle context did not cancel")
	}
	if !expired() {
		t.Fatal("expired = false, want true")
	}
}

func TestStreamIdleContextResetExtendsDeadline(t *testing.T) {
	const timeout = 40 * time.Millisecond
	ctx, reset, stop, expired := newStreamIdleContext(context.Background(), timeout)
	defer stop()

	time.Sleep(25 * time.Millisecond)
	reset()
	select {
	case <-ctx.Done():
		t.Fatal("stream idle context canceled before the reset deadline")
	case <-time.After(25 * time.Millisecond):
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("stream idle context did not cancel after the reset deadline")
	}
	if !expired() {
		t.Fatal("expired = false, want true")
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

	p := NewAnthropic(testProfile(t, Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test"}), nil)
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

	p := NewOpenAI(testBlockingProfile(t, Config{
		Protocol: string(ProtocolOpenAIChat),
		BaseURL:  srv.URL,
		APIKey:   "test-key",
		Model:    "gpt-test",
	}), nil)

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

	p := NewOpenAI(testProfile(t, Config{Protocol: string(ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
	var deltas []StreamDelta
	resp, err := CompleteWithOptions(context.Background(), p, "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		OnDelta: func(delta StreamDelta) { deltas = append(deltas, delta) },
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	streamOptions, _ := capturedBody["stream_options"].(map[string]any)
	if capturedBody["stream"] != true || streamOptions["include_usage"] != true {
		t.Fatalf("stream request = %+v", capturedBody)
	}
	wantDeltas := []StreamDelta{
		{Kind: "reasoning", Index: 0, Text: "plan "},
		{Kind: "text", Index: 0, Text: "hel"},
		{Kind: "text", Index: 0, Text: "lo"},
	}
	if !slices.Equal(deltas, wantDeltas) {
		t.Fatalf("deltas = %+v, want %+v", deltas, wantDeltas)
	}
	if resp.Message.FirstText() != "hello" || resp.StopReason != StopToolUse {
		t.Fatalf("response = %+v", resp)
	}
	if calls := resp.Message.ToolCalls(); len(calls) != 1 || calls[0].ToolName != "read" || calls[0].Input["path"] != "x" {
		t.Fatalf("tool calls = %+v", calls)
	}
	if len(resp.Message.Blocks) < 2 || resp.Message.Blocks[0].Type != BlockReasoning || resp.Message.Blocks[0].Text != "plan " {
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

	p := NewOpenAI(testProfile(t, Config{Protocol: string(ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
	_, err := CompleteWithOptions(context.Background(), p, "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{StreamIdleTimeout: 20 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("err = %v, want stream idle timeout", err)
	}
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

	p := NewOpenAI(testBlockingProfile(t, Config{
		Protocol: string(ProtocolOpenAIChat),
		BaseURL:  srv.URL,
		APIKey:   "k",
		Model:    "m",
	}), nil)
	schema := map[string]any{"type": "object"}

	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hello")}, []ToolSpec{
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
	assertEmptyProperties(t, params)
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

	p := NewOpenAI(testBlockingProfile(t, Config{
		Protocol: string(ProtocolOpenAIChat),
		BaseURL:  srv.URL,
		APIKey:   "k",
		Model:    "m",
	}), nil)
	history := []Message{
		TextMessage(RoleUser, "check status"),
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "call_1",
			ToolName:  "mcp__chanwire__chanwire_status",
		}}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "call_1", Content: "ok"}}},
		TextMessage(RoleUser, "hello"),
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
				return NewAnthropic(testProfile(t, Config{ID: "anthropic", BaseURL: baseURL, APIKey: "test-key", Model: "claude-test"}), nil)
			},
			serverErr:          `{"type":"error","error":{"type":"api_error","message":"temporary server error"}}`,
			badReqErr:          `{"type":"error","error":{"type":"invalid_request_error","message":"bad request"}}`,
			successContentType: "text/event-stream",
			successRes:         anthropicTextStreamData("claude-test", "ok", "end_turn", 1, 1),
		},
		{
			name: "openai",
			provider: func(baseURL string) Provider {
				return NewOpenAI(testBlockingProfile(t, Config{Protocol: string(ProtocolOpenAIChat), BaseURL: baseURL, APIKey: "k", Model: "m"}), nil)
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
		{
			name: "openai-codex",
			provider: func(baseURL string) Provider {
				return NewOpenAICodexResponses(testProfile(t, Config{ID: "openai-codex", Protocol: string(ProtocolOpenAICodexResponses), BaseURL: baseURL, APIKey: "k", Model: "m"}), nil)
			},
			serverErr:          `{"error":{"message":"temporary server error","type":"server_error"}}`,
			badReqErr:          `{"error":{"message":"bad request","type":"invalid_request_error"}}`,
			successContentType: "text/event-stream",
			successRes:         `data: {"type":"response.completed","response":{"id":"resp_1","model":"m","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}]}}` + "\n\n",
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

func TestOpenAICodexResponses_RetriesTransportError(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
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
	p := NewOpenAICodexResponses(testProfile(t, Config{ID: "openai-codex", Protocol: string(ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)

	resp, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
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
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       errReadCloser{err: io.ErrUnexpectedEOF},
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
	p := NewOpenAICodexResponses(testProfile(t, Config{ID: "openai-codex", Protocol: string(ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)

	resp, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
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

func TestOpenAICodexResponses_RetriesSSEReadInternalErrorByCategory(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{
				StatusCode: http.StatusOK,
				Status:     "200 OK",
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       errReadCloser{err: errors.New("stream error: stream ID 37; INTERNAL_ERROR; received from peer")},
				Request:    r,
			}, nil
		}
		return codexCompletedTextResponse(r, "ok"), nil
	})}
	p := NewOpenAICodexResponses(testProfile(t, Config{ID: "openai-codex", Protocol: string(ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)
	withOpts, ok := p.(ProviderWithOptions)
	if !ok {
		t.Fatal("openai-codex provider does not implement ProviderWithOptions")
	}
	var diagnostics []ProviderRetryDiagnostic

	resp, err := withOpts.CompleteWithOptions(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		RetryObserver: func(d ProviderRetryDiagnostic) {
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
	if !got.WillRetry || got.Exhausted || got.Attempt != 1 || got.MaxAttempts != providerMaxRetries+1 || got.DelayMS <= 0 {
		t.Fatalf("retry diagnostic = %+v", got)
	}
	if got.Provider != "openai-codex" || got.Model != "m" || got.Protocol != ProtocolOpenAICodexResponses || got.Transport != CodexTransportSSE {
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
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				attempts++
				return &http.Response{
					StatusCode: http.StatusOK,
					Status:     "200 OK",
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       errReadCloser{err: tc.err},
					Request:    r,
				}, nil
			})}
			p := NewOpenAICodexResponses(testProfile(t, Config{ID: "openai-codex", Protocol: string(ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)
			withOpts := p.(ProviderWithOptions)
			var diagnostics []ProviderRetryDiagnostic

			_, err := withOpts.CompleteWithOptions(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
				RetryObserver: func(d ProviderRetryDiagnostic) {
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
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       errReadCloser{err: errors.New("stream error: stream ID 9; INTERNAL_ERROR; received from peer")},
			Request:    r,
		}, nil
	})}
	p := NewOpenAICodexResponses(testProfile(t, Config{ID: "openai-codex", Protocol: string(ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)
	withOpts := p.(ProviderWithOptions)
	var diagnostics []ProviderRetryDiagnostic

	_, err := withOpts.CompleteWithOptions(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		RetryObserver: func(d ProviderRetryDiagnostic) {
			diagnostics = append(diagnostics, d)
		},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "codex SSE retry exhausted after 11 attempts") || !strings.Contains(err.Error(), "INTERNAL_ERROR") {
		t.Fatalf("err = %v", err)
	}
	if attempts != providerMaxRetries+1 {
		t.Fatalf("attempts = %d, want %d", attempts, providerMaxRetries+1)
	}
	if len(diagnostics) != providerMaxRetries+1 {
		t.Fatalf("diagnostics len = %d, want %d: %+v", len(diagnostics), providerMaxRetries+1, diagnostics)
	}
	final := diagnostics[len(diagnostics)-1]
	if final.WillRetry || !final.Exhausted || final.Attempt != providerMaxRetries+1 || final.DelayMS != 0 {
		t.Fatalf("final diagnostic = %+v", final)
	}
}

func TestOpenAICodexResponses_DoesNotRetrySemanticResponseFailure(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(
				`data: {"type":"response.failed","response":{"id":"resp_1","model":"m","status":"failed","error":{"message":"semantic failure","code":"server_error"}}}` + "\n\n",
			)),
			Request: r,
		}, nil
	})}
	p := NewOpenAICodexResponses(testProfile(t, Config{ID: "openai-codex", Protocol: string(ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)

	_, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
	if err == nil || !strings.Contains(err.Error(), "semantic failure") {
		t.Fatalf("err = %v, want semantic failure", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestOpenAICodexResponses_RetriesStreamClosedBeforeCompleted(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
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
	p := NewOpenAICodexResponses(testProfile(t, Config{ID: "openai-codex", Protocol: string(ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)

	resp, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
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
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
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
	p := NewOpenAICodexResponses(testProfile(t, Config{ID: "openai-codex", Protocol: string(ProtocolOpenAICodexResponses), BaseURL: "https://chatgpt.com/backend-api/codex", APIKey: "k", Model: "m"}), client)
	withOpts := p.(ProviderWithOptions)
	var deltas []StreamDelta

	resp, err := withOpts.CompleteWithOptions(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		OnDelta: func(delta StreamDelta) {
			deltas = append(deltas, delta)
		},
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if resp.Message.FirstText() != "hi there" {
		t.Fatalf("text = %q, want hi there", resp.Message.FirstText())
	}
	want := []StreamDelta{
		{Kind: "text", Index: 0, Text: "hi "},
		{Kind: "text", Index: 0, Text: "there"},
	}
	if !slices.Equal(deltas, want) {
		t.Fatalf("deltas = %+v, want %+v", deltas, want)
	}
}

func codexCompletedTextResponse(r *http.Request, text string) *http.Response {
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

	p := NewOpenAI(testBlockingProfile(t, Config{Protocol: string(ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
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

	p := NewOpenAI(testBlockingProfile(t, Config{Protocol: string(ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
	hist := []Message{
		TextMessage(RoleUser, "hello"),
		{Role: RoleAssistant, Blocks: []Block{{Type: BlockReasoning, Text: "thought"}}},
		TextMessage(RoleUser, "again"),
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

	p := NewOpenAI(testBlockingProfile(t, Config{Protocol: string(ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)

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

func TestOpenAI_ProjectsUserAndToolResultImages(t *testing.T) {
	media := testImageMedia(t)
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

	p := NewOpenAI(testBlockingProfile(t, Config{
		Protocol:     string(ProtocolOpenAIChat),
		BaseURL:      srv.URL,
		APIKey:       "k",
		Model:        "m",
		Capabilities: CapabilityOverrides{Vision: boolPtr(true)},
	}), nil)
	hist := []Message{
		{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "look"}, {Type: BlockImage, Media: media}}},
		{Role: RoleAssistant, Blocks: []Block{
			{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "render_chart", Input: map[string]any{"id": "x"}},
			{Type: BlockToolUse, ToolUseID: "call_2", ToolName: "read", Input: map[string]any{"path": "x"}},
		}},
		{Role: RoleUser, Blocks: []Block{
			{Type: BlockToolResult, ToolUseID: "call_1", ToolName: "render_chart", Content: "chart", Media: media},
			{Type: BlockToolResult, ToolUseID: "call_2", ToolName: "read", Content: "file"},
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
}

func TestNewProvider_Errors(t *testing.T) {
	if _, err := New(Config{ID: "anthropic", APIKey: "", Model: "m"}); err == nil {
		t.Error("missing key should error")
	}
	if _, err := New(Config{ID: "anthropic", APIKey: "k"}); err == nil {
		t.Error("missing model should error")
	}
	if _, err := New(Config{ID: "bogus", APIKey: "k", Model: "m"}); err == nil {
		t.Error("unknown provider selector should error")
	}
	if _, err := NewProvider(ProviderProfile{
		ID:       "openai-codex",
		Protocol: ProtocolOpenAICodexResponses,
		APIKey:   "k",
		Model:    "m",
		Compat:   CompatOptions{CodexTransport: "bogus"},
	}); err == nil {
		t.Error("unsupported codex transport should error")
	}
}

func TestNewProvider_FromResolvedProfile(t *testing.T) {
	profile, err := ResolveProfile(Config{
		ID:       "openai",
		Protocol: string(ProtocolOpenAIResponses),
		APIKey:   "sk-test",
		Model:    "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewProvider(profile)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "openai:gpt-test" {
		t.Fatalf("provider name = %q", provider.Name())
	}
}

func TestNewProvider_DoesNotReResolveProfile(t *testing.T) {
	profile := ProviderProfile{
		ID:       "deepseek",
		Protocol: ProtocolOpenAIResponses,
		BaseURL:  "http://127.0.0.1:12345",
		APIKey:   "sk-test",
		Model:    "responses-test",
		Headers:  map[string]string{"X-Provider": "canonical"},
		Query:    map[string]string{"debug": "1"},
		Capabilities: ProviderCapabilities{
			Tools:           true,
			ReasoningReplay: true,
		},
		Compat: CompatOptions{ReasoningReplayFields: []string{"canonical_reasoning"}},
	}
	provider, err := NewProvider(profile)
	if err != nil {
		t.Fatal(err)
	}
	if provider.Name() != "deepseek:responses-test" {
		t.Fatalf("provider name = %q", provider.Name())
	}
	responsesProvider, ok := provider.(*openAIResponsesProvider)
	if !ok {
		t.Fatalf("provider type = %T, want *openAIResponsesProvider", provider)
	}
	if responsesProvider.profile.Headers["X-Provider"] != "canonical" ||
		responsesProvider.profile.Query["debug"] != "1" ||
		!responsesProvider.profile.Capabilities.ReasoningReplay ||
		len(responsesProvider.profile.Compat.ReasoningReplayFields) != 1 ||
		responsesProvider.profile.Compat.ReasoningReplayFields[0] != "canonical_reasoning" {
		t.Fatalf("profile was not preserved: %+v", responsesProvider.profile)
	}
}

func TestOpenAICodexResponses_RejectsUnsupportedResolvedTransport(t *testing.T) {
	p := NewOpenAICodexResponses(ProviderProfile{
		ID:       "openai-codex",
		Protocol: ProtocolOpenAICodexResponses,
		APIKey:   "k",
		Model:    "m",
		Compat:   CompatOptions{CodexTransport: "bogus"},
	}, nil)
	_, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
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

	p := NewOpenAI(testBlockingProfile(t, Config{
		Protocol:       string(ProtocolOpenAIChat),
		BaseURL:        srv.URL,
		APIKey:         "k",
		Model:          "m",
		ThinkingEffort: "low",
	}), nil)
	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if capturedBody["reasoning_effort"] != "low" {
		t.Errorf("reasoning_effort = %v, want %q", capturedBody["reasoning_effort"], "low")
	}
}

func TestOpenAI_DeepSeekPresetEnablesThinkingEffort(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewOpenAI(testBlockingProfile(t, Config{
		ID:             "deepseek",
		BaseURL:        srv.URL,
		APIKey:         "k",
		Model:          "deepseek-v4-pro",
		ThinkingEffort: "max",
	}), nil)
	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if capturedBody["reasoning_effort"] != "max" {
		t.Errorf("reasoning_effort = %v, want %q", capturedBody["reasoning_effort"], "max")
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

	p := NewOpenAI(testBlockingProfile(t, Config{Protocol: string(ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
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

	p := NewOpenAI(testBlockingProfile(t, Config{Protocol: string(ProtocolOpenAIChat), BaseURL: srv.URL, APIKey: "k", Model: "m"}), nil)
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

func TestOpenAI_CompleteOptionsSendsPromptCacheKeyAndRecordsCachedTokens(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{
			"id":"chatcmpl_1","object":"chat.completion","model":"gpt-test",
			"choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],
			"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,"prompt_tokens_details":{"cached_tokens":80}}
		}`))
	}))
	defer srv.Close()

	p, err := New(blockingConfig(Config{ID: "openai-chat-test", Protocol: "openai/chat", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := CompleteWithOptions(context.Background(), p, "sys", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		CachePolicy: CachePolicy{StablePrefixKey: "juex-cache-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedBody["prompt_cache_key"] != "juex-cache-key" {
		t.Fatalf("prompt_cache_key = %+v", capturedBody["prompt_cache_key"])
	}
	if resp.Usage.CachedInputTokens != 80 {
		t.Fatalf("cached input tokens = %d", resp.Usage.CachedInputTokens)
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
	p := NewOpenAI(testBlockingProfile(t, Config{
		Protocol:       string(ProtocolOpenAIChat),
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
	}), nil)
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

	p, err := New(blockingConfig(Config{
		ID:             "openai",
		Protocol:       "openai/responses",
		BaseURL:        srv.URL,
		APIKey:         "k",
		Model:          "gpt-test",
		ThinkingEffort: "low",
		Headers:        map[string]string{"X-Juex-Test": "yes"},
		Query:          map[string]string{"trace": "1"},
	}))
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
	if resp.Message.Blocks[0].Text != "thought summary" || !resp.Message.Blocks[0].Redacted || resp.Message.Blocks[0].Content == "" {
		t.Fatalf("reasoning summary/replay metadata = %+v", resp.Message.Blocks[0])
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
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

	p, err := New(Config{ID: "openai", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []StreamDelta
	resp, err := CompleteWithOptions(context.Background(), p, "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		OnDelta: func(delta StreamDelta) { deltas = append(deltas, delta) },
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	if capturedBody["stream"] != true {
		t.Fatalf("stream request = %+v", capturedBody)
	}
	wantDeltas := []StreamDelta{
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

func TestOpenAIResponses_StreamIdleTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer srv.Close()

	p, err := New(Config{ID: "openai", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CompleteWithOptions(context.Background(), p, "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{StreamIdleTimeout: 20 * time.Millisecond})
	if err == nil || !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("err = %v, want stream idle timeout", err)
	}
}

func TestOpenAIResponses_ProjectsUserImageAndToolResultImageReference(t *testing.T) {
	media := testImageMedia(t)
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

	p, err := New(blockingConfig(Config{
		ID:           "openai",
		Protocol:     "openai/responses",
		BaseURL:      srv.URL,
		APIKey:       "k",
		Model:        "gpt-test",
		Capabilities: CapabilityOverrides{Vision: boolPtr(true)},
	}))
	if err != nil {
		t.Fatal(err)
	}
	hist := []Message{
		{Role: RoleUser, Blocks: []Block{{Type: BlockText, Text: "look"}, {Type: BlockImage, Media: media}}},
		{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "render_chart", Input: map[string]any{"id": "x"}}}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "call_1", ToolName: "render_chart", Content: "chart", Media: media}}},
	}
	if _, err := p.Complete(context.Background(), "", hist, []ToolSpec{{Name: "render_chart", Schema: map[string]any{"type": "object"}}}); err != nil {
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

	p, err := New(blockingConfig(Config{
		ID:       "openai",
		Protocol: "openai/responses",
		BaseURL:  srv.URL,
		APIKey:   "k",
		Model:    "gpt-test",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hello")}, []ToolSpec{
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
	assertEmptyProperties(t, params)
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
			"usage":{"input_tokens":120,"output_tokens":6,"total_tokens":126,"input_tokens_details":{"cached_tokens":96}}
		}`))
	}))
	defer srv.Close()

	p, err := New(blockingConfig(Config{ID: "openai", Protocol: "openai/responses", BaseURL: srv.URL, APIKey: "k", Model: "gpt-test"}))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := CompleteWithOptions(context.Background(), p, "sys", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		CachePolicy: CachePolicy{StablePrefixKey: "juex-cache-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if capturedBody["prompt_cache_key"] != "juex-cache-key" {
		t.Fatalf("prompt_cache_key = %+v", capturedBody["prompt_cache_key"])
	}
	if resp.Usage.CachedInputTokens != 96 {
		t.Fatalf("cached input tokens = %d", resp.Usage.CachedInputTokens)
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

	p, err := New(blockingConfig(Config{
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
		[]Message{
			TextMessage(RoleUser, "first"),
			{Role: RoleAssistant, Blocks: []Block{
				{Type: BlockReasoning, Signature: "rs_prev", Content: "enc_prev", Redacted: true},
				{Type: BlockText, Text: "first answer"},
			}},
			TextMessage(RoleUser, "second"),
		},
		nil,
	)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	input, _ := capturedBody["input"].([]any)
	for _, item := range input {
		obj, _ := item.(map[string]any)
		if obj["type"] != "reasoning" {
			continue
		}
		if _, ok := obj["summary"]; !ok {
			t.Fatalf("reasoning replay omitted required summary field: %+v", obj)
		}
		summary, ok := obj["summary"].([]any)
		if !ok || len(summary) != 0 {
			t.Fatalf("reasoning summary = %#v, want empty array", obj["summary"])
		}
		return
	}
	t.Fatalf("reasoning replay item not found in input: %+v", input)
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

	p, err := New(Config{
		ID:             "openai-codex",
		Protocol:       "openai-codex/responses",
		BaseURL:        srv.URL,
		APIKey:         "codex-token",
		Model:          "gpt-test",
		ThinkingEffort: "low",
		Headers:        map[string]string{"ChatGPT-Account-ID": "acct_1"},
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
	if capturedAuth != "Bearer codex-token" || capturedAccount != "acct_1" || capturedOriginator != "juex" {
		t.Fatalf("headers auth/account/originator = %q/%q/%q", capturedAuth, capturedAccount, capturedOriginator)
	}
	if capturedBeta != "responses=experimental" || capturedAccept != "text/event-stream" {
		t.Fatalf("headers beta/accept = %q/%q", capturedBeta, capturedAccept)
	}
	if capturedBody["model"] != "gpt-test" || capturedBody["instructions"] != "system" || capturedBody["stream"] != true {
		t.Fatalf("captured body = %+v", capturedBody)
	}
	if capturedBody["reasoning"] == nil || capturedBody["include"] == nil {
		t.Fatalf("codex request should include reasoning controls: %+v", capturedBody)
	}
	input, _ := capturedBody["input"].([]any)
	if len(input) < 3 {
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
	if resp.Message.Blocks[0].Text != "thought summary" || !resp.Message.Blocks[0].Redacted || resp.Message.Blocks[0].Content == "" {
		t.Fatalf("codex reasoning summary/replay metadata = %+v", resp.Message.Blocks[0])
	}
	if resp.Usage.InputTokens != 10 || resp.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
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

	p, err := New(Config{
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
		[]Message{
			TextMessage(RoleUser, "first"),
			{Role: RoleAssistant, Blocks: []Block{
				{Type: BlockReasoning, Text: "prior", Signature: "rs_prev", Content: "enc_prev", Redacted: true},
				{Type: BlockText, Text: "first answer"},
			}},
			TextMessage(RoleUser, "second"),
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

	p, err := New(Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hello")}, []ToolSpec{
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
	assertEmptyProperties(t, params)
}

func TestOpenAICodexResponses_CompleteOptionsSendsPromptCacheKeyAndRecordsCachedTokens(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-test","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"ok","annotations":[]}]}],"usage":{"input_tokens":140,"output_tokens":7,"total_tokens":147,"input_tokens_details":{"cached_tokens":112}}}}`+"\n\n")
	}))
	defer srv.Close()

	p, err := New(Config{ID: "openai-codex", Protocol: "openai-codex/responses", BaseURL: srv.URL, APIKey: "codex-token", Model: "gpt-test"})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := CompleteWithOptions(context.Background(), p, "sys", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		CachePolicy: CachePolicy{StablePrefixKey: "juex-cache-key", Retention: "24h"},
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
	if resp.Usage.CachedInputTokens != 112 {
		t.Fatalf("cached input tokens = %d", resp.Usage.CachedInputTokens)
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
		if !isWebsocketRequest(r) {
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
		if err := conn.Write(ctx, websocket.MessageText, codexCompletedWebsocketEvent("resp_1", "ok")); err != nil {
			serverErrs <- err
		}
	}))
	defer srv.Close()

	p, err := New(Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
		Headers:  map[string]string{"ChatGPT-Account-ID": "acct_1"},
		Query:    map[string]string{"trace": "1"},
		Compat:   CompatOptions{CodexTransport: CodexTransportWebSocketCached},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), "system", []Message{TextMessage(RoleUser, "hello")}, nil)
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
			codexCompletedWebsocketEvent("resp_1", "ok"),
		} {
			if err := conn.Write(ctx, websocket.MessageText, event); err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	p, err := New(Config{
		ID:      "openai-codex",
		BaseURL: srv.URL,
		APIKey:  "codex-token",
		Model:   "gpt-test",
		Compat:  CompatOptions{CodexTransport: CodexTransportWebSocket},
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []StreamDelta
	resp, err := CompleteWithOptions(context.Background(), p, "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		OnDelta: func(delta StreamDelta) { deltas = append(deltas, delta) },
	})
	if err != nil {
		t.Fatalf("CompleteWithOptions: %v", err)
	}
	want := []StreamDelta{
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

	p, err := New(Config{
		ID:      "openai-codex",
		BaseURL: srv.URL,
		APIKey:  "codex-token",
		Model:   "gpt-test",
		Compat:  CompatOptions{CodexTransport: CodexTransportWebSocket},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = CompleteWithOptions(context.Background(), p, "", []Message{TextMessage(RoleUser, "hi")}, nil, CompleteOptions{
		StreamIdleTimeout: 20 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "idle timeout") {
		t.Fatalf("err = %v, want websocket idle timeout", err)
	}
}

func TestOpenAICodexResponses_WebsocketCachedUsesPreviousResponseIDForIncrementalInput(t *testing.T) {
	frames := make(chan map[string]any, 2)
	serverErrs := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isWebsocketRequest(r) {
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
			codexCompletedWebsocketEvent("resp_1", "first answer"),
			codexCompletedWebsocketEvent("resp_2", "second answer"),
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

	p, err := New(Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
		Compat:   CompatOptions{CodexTransport: CodexTransportWebSocketCached},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstHistory := []Message{TextMessage(RoleUser, "first")}
	firstResp, err := p.Complete(context.Background(), "", firstHistory, nil)
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	secondHistory := append(append([]Message{}, firstHistory...), firstResp.Message, TextMessage(RoleUser, "second"))
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
		if !isWebsocketRequest(r) {
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
		if err := conn.Write(ctx, websocket.MessageText, codexCompletedWebsocketEvent("resp_ws", "ok")); err != nil {
			serverErrs <- err
		}
	}))
	defer srv.Close()

	p, err := New(Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
		Compat:   CompatOptions{CodexTransport: CodexTransportWebSocket},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstHistory := []Message{TextMessage(RoleUser, "first")}
	firstResp, err := p.Complete(context.Background(), "", firstHistory, nil)
	if err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	secondHistory := append(append([]Message{}, firstHistory...), firstResp.Message, TextMessage(RoleUser, "second"))
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
		if isWebsocketRequest(r) {
			wsAttempts++
			http.Error(w, "websocket unavailable", http.StatusServiceUnavailable)
			return
		}
		sseAttempts++
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\n", codexCompletedWebsocketEvent("resp_sse", "sse ok"))
	}))
	defer srv.Close()

	p, err := New(Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
		Compat:   CompatOptions{CodexTransport: CodexTransportAuto},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
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

	p, err := New(Config{
		ID:       "openai-codex",
		Protocol: "openai-codex/responses",
		BaseURL:  srv.URL,
		APIKey:   "codex-token",
		Model:    "gpt-test",
		Compat:   CompatOptions{CodexTransport: CodexTransportWebSocketCached},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil)
	if err == nil || !strings.Contains(err.Error(), "response.completed") {
		t.Fatalf("err = %v, want response.completed close error", err)
	}
}

func isWebsocketRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

func codexCompletedWebsocketEvent(id, text string) []byte {
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

func TestAnthropic_ThinkingEffort(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(buf, &capturedBody)
		writeAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 1)
	}))
	defer srv.Close()

	p := NewAnthropic(testProfile(t, Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test", ThinkingEffort: "low"}), nil)
	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil); err != nil {
		t.Fatalf("Complete: %v", err)
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
		writeAnthropicTextStream(w, "claude-test", "ok", "end_turn", 10, 1)
	}))
	defer srv.Close()

	p := NewAnthropic(testProfile(t, Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "claude-test"}), nil)
	if _, err := p.Complete(context.Background(), "", []Message{TextMessage(RoleUser, "hi")}, nil); err != nil {
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

	p := NewAnthropic(testProfile(t, Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "minimax-m2.7"}), nil)
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

	p := NewAnthropic(testProfile(t, Config{ID: "anthropic", BaseURL: srv.URL, APIKey: "test-key", Model: "minimax-m2.7", ThinkingEffort: "high"}), nil)
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

func assertEmptyProperties(t *testing.T, schema map[string]any) {
	t.Helper()
	assertObjectWithEmptyProperties(t, schema)
	if schema["additionalProperties"] != false {
		t.Fatalf("additionalProperties = %+v, want false in empty object schema %+v", schema["additionalProperties"], schema)
	}
}

func assertObjectWithEmptyProperties(t *testing.T, schema map[string]any) {
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

func schemaValueMap(t *testing.T, value any) map[string]any {
	t.Helper()
	out, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("schema value = %#v, want map[string]any", value)
	}
	return out
}

func TestToolCallArgumentsUsesEmptyObjectForNilInput(t *testing.T) {
	if got := toolCallArguments("", nil); got != "{}" {
		t.Fatalf("nil arguments = %q, want {}", got)
	}
	if got := toolCallArguments("", map[string]any{"path": "x"}); got != `{"path":"x"}` {
		t.Fatalf("map arguments = %q", got)
	}
	if got := toolCallArguments("", map[string]any{"_raw_arguments": `{"path":"x"}`}); got != `{"path":"x"}` {
		t.Fatalf("decoded raw arguments = %q, want structured path", got)
	}
	if got := toolCallArguments("", map[string]any{"_raw_arguments": `{"path":"unterminated`}); got != "{}" {
		t.Fatalf("malformed raw arguments = %q, want sanitized empty object", got)
	}
	if got := toolCallArguments("", map[string]any{"_raw_arguments": `null`}); got != "{}" {
		t.Fatalf("null raw arguments = %q, want sanitized empty object", got)
	}
	if got := toolCallArguments("", map[string]any{"_raw_arguments": `"null"`}); got != "{}" {
		t.Fatalf("encoded null raw arguments = %q, want sanitized empty object", got)
	}
}

func TestBuildProviderContextOwnsProjectionAndValidation(t *testing.T) {
	history := []Message{
		{
			Role: RoleAssistant,
			Blocks: []Block{
				{Type: BlockReasoning, Text: "hidden", Signature: "rs_1"},
				{
					Type:      BlockToolUse,
					ToolUseID: "call_1",
					ToolName:  "read",
					Input:     map[string]any{"_raw_arguments": `{"path":"README.md"}`},
				},
			},
		},
		{
			Role: RoleUser,
			Blocks: []Block{{
				Type:      BlockToolResult,
				ToolUseID: "call_1",
				Content:   "file",
			}},
		},
	}
	providerContext, err := BuildProviderContext(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true, ReasoningReplay: true},
	}, ProviderContextOptions{OmitReasoning: true})
	if err != nil {
		t.Fatalf("BuildProviderContext: %v", err)
	}
	if len(providerContext.Messages) != 2 {
		t.Fatalf("message count = %d, want 2", len(providerContext.Messages))
	}
	blocks := providerContext.Messages[0].Blocks
	if len(blocks) != 1 || blocks[0].Type != BlockToolUse {
		t.Fatalf("assistant blocks = %+v, want only projected tool use", blocks)
	}
	if blocks[0].Input["_raw_arguments"] != nil || blocks[0].Input["path"] != "README.md" {
		t.Fatalf("tool input = %+v, want decoded provider-visible input", blocks[0].Input)
	}
}

func TestBuildProviderContextValidatesAfterCapabilityFiltering(t *testing.T) {
	history := []Message{
		{
			Role: RoleAssistant,
			Blocks: []Block{{
				Type:      BlockToolUse,
				ToolUseID: "call_missing",
				ToolName:  "read",
				Input:     map[string]any{"path": "README.md"},
			}},
		},
	}
	if _, err := BuildProviderContext(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, ProviderContextOptions{}); err == nil {
		t.Fatal("expected missing tool_result error when tools are provider-visible")
	}
	providerContext, err := BuildProviderContext(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: false},
	}, ProviderContextOptions{})
	if err != nil {
		t.Fatalf("tool blocks filtered by capability should not fail provider-visible validation: %v", err)
	}
	if len(providerContext.Messages) != 0 {
		t.Fatalf("provider-visible messages = %+v, want empty after filtering and compaction", providerContext.Messages)
	}
}

func TestProjectProviderTranscriptPreservesWriteChunkContent(t *testing.T) {
	history := []Message{{
		Role: RoleAssistant,
		Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "chunk_1",
			ToolName:  "write_chunk",
			Input: map[string]any{
				"write_id": "write-abc",
				"index":    2,
				"content":  strings.Repeat("x", 128),
				"sha256":   "abc123",
			},
		}},
	}}
	projected := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	input := projected[0].Blocks[0].Input
	if input["content"] != strings.Repeat("x", 128) {
		t.Fatalf("projected write_chunk input should keep content: %+v", input)
	}
	if input["write_id"] != "write-abc" || input["index"] != 2 {
		t.Fatalf("projected input lost metadata: %+v", input)
	}
	if history[0].Blocks[0].Input["content"] == nil {
		t.Fatalf("projection mutated original history: %+v", history[0].Blocks[0].Input)
	}
	if input := ProviderToolInput("other_tool", map[string]any{"write_id": "x", "index": 0, "content": "keep"}); input["content"] != "keep" {
		t.Fatalf("non-write_chunk input should keep content: %+v", input)
	}
}

func TestProjectProviderTranscriptFoldsCommittedChunkedWriteSession(t *testing.T) {
	const writeID = "write-committed"
	chunks := []string{
		strings.Repeat("alpha ", 20),
		strings.Repeat("beta ", 20),
		strings.Repeat("gamma ", 20),
	}
	history := []Message{
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "begin_1",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "reports/long.md", "mode": "create"},
		}}},
		{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "begin_1",
			Content:   "write_begin: write_id=" + writeID + " path=reports/long.md mode=create max_chunk_bytes=4000 max_chunk_chars=2000 recommended_chunk_bytes=4000 recommended_chunk_chars=2000",
			ChunkedWrite: &chunkedwrite.Event{
				Kind:    chunkedwrite.EventBegin,
				WriteID: writeID,
				Path:    "reports/long.md",
				Mode:    chunkedwrite.ModeCreate,
			},
		}}},
	}
	for i, chunk := range chunks {
		toolUseID := fmt.Sprintf("chunk_%d", i)
		history = append(history,
			Message{Role: RoleAssistant, Blocks: []Block{{
				Type:      BlockToolUse,
				ToolUseID: toolUseID,
				ToolName:  "write_chunk",
				Input:     map[string]any{"write_id": writeID, "index": i, "content": chunk},
			}}},
			Message{Role: RoleUser, Blocks: []Block{{
				Type:      BlockToolResult,
				ToolUseID: toolUseID,
				Content:   fmt.Sprintf("write_chunk: write_id=%s index=%d bytes=%d chars=%d sha256=hash-%d chunks=%d duplicate=false", writeID, i, len(chunk), len(chunk), i, i+1),
				ChunkedWrite: &chunkedwrite.Event{
					Kind:    chunkedwrite.EventChunk,
					WriteID: writeID,
					Index:   i,
					Bytes:   len(chunk),
					Chars:   len(chunk),
					SHA256:  fmt.Sprintf("hash-%d", i),
					Chunks:  i + 1,
				},
			}}},
		)
	}
	history = append(history,
		Message{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "commit_1",
			ToolName:  "write_commit",
			Input:     map[string]any{"write_id": writeID, "expected_chunks": len(chunks)},
		}}},
		Message{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "commit_1",
			Content:   "write_commit: write_id=write-committed path=reports/long.md bytes=320 chars=320 chunks=3 sha256=final-hash",
			ChunkedWrite: &chunkedwrite.Event{
				Kind:    chunkedwrite.EventCommit,
				WriteID: writeID,
				Path:    "reports/long.md",
				Bytes:   320,
				Chars:   320,
				Chunks:  3,
				SHA256:  "final-hash",
			},
		}}},
	)

	projected := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	text := providerProjectionDebugString(projected)
	if !strings.Contains(text, "Chunked write provider replay summary: committed") {
		t.Fatalf("projected history missing committed summary:\n%s", text)
	}
	if !strings.Contains(text, "path=reports/long.md") || !strings.Contains(text, "sha256=final-hash") {
		t.Fatalf("committed summary missing file metadata:\n%s", text)
	}
	for _, chunk := range chunks {
		if strings.Contains(text, chunk) {
			t.Fatalf("committed projection should fold raw chunk content %q:\n%s", chunk, text)
		}
	}
	if got := providerProjectionToolUseNames(projected); len(got) != 0 {
		t.Fatalf("committed projection should omit chunked write tool calls, got %+v", got)
	}
	if strings.Contains(text, "content_omitted") {
		t.Fatalf("committed projection should not use fake tool arguments:\n%s", text)
	}
}

func TestProjectProviderTranscriptFoldsCommittedChunkedWriteFromLifecycleFacts(t *testing.T) {
	const writeID = "write-fact"
	chunks := []string{
		strings.Repeat("alpha ", 20),
		strings.Repeat("beta ", 20),
	}
	history := []Message{
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "begin_fact",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "reports/fact.md", "mode": "create"},
		}}},
		{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "begin_fact",
			Content:   "presentation text changed",
			ChunkedWrite: &chunkedwrite.Event{
				Kind:    chunkedwrite.EventBegin,
				WriteID: writeID,
				Path:    "reports/fact.md",
				Mode:    chunkedwrite.ModeCreate,
			},
		}}},
	}
	for i, chunk := range chunks {
		toolUseID := fmt.Sprintf("chunk_fact_%d", i)
		history = append(history,
			Message{Role: RoleAssistant, Blocks: []Block{{
				Type:      BlockToolUse,
				ToolUseID: toolUseID,
				ToolName:  "write_chunk",
				Input:     map[string]any{"write_id": writeID, "index": i, "content": chunk},
			}}},
			Message{Role: RoleUser, Blocks: []Block{{
				Type:      BlockToolResult,
				ToolUseID: toolUseID,
				Content:   "chunk accepted",
				ChunkedWrite: &chunkedwrite.Event{
					Kind:    chunkedwrite.EventChunk,
					WriteID: writeID,
					Index:   i,
					Bytes:   len(chunk),
					Chars:   len(chunk),
					Chunks:  i + 1,
				},
			}}},
		)
	}
	history = append(history,
		Message{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "commit_fact",
			ToolName:  "write_commit",
			Input:     map[string]any{"write_id": writeID, "expected_chunks": len(chunks)},
		}}},
		Message{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "commit_fact",
			Content:   "commit presentation changed",
			ChunkedWrite: &chunkedwrite.Event{
				Kind:    chunkedwrite.EventCommit,
				WriteID: writeID,
				Path:    "reports/fact.md",
				Bytes:   111,
				Chars:   111,
				Chunks:  len(chunks),
				SHA256:  "full-hash",
			},
		}}},
	)

	projected := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	text := providerProjectionDebugString(projected)
	if !strings.Contains(text, "Chunked write provider replay summary: committed") {
		t.Fatalf("projected history missing committed summary from facts:\n%s", text)
	}
	if !strings.Contains(text, "path=reports/fact.md") || !strings.Contains(text, "sha256=full-hash") {
		t.Fatalf("summary missing fact metadata:\n%s", text)
	}
	for _, chunk := range chunks {
		if strings.Contains(text, chunk) {
			t.Fatalf("fact projection should fold raw chunk content %q:\n%s", chunk, text)
		}
	}
}

func TestProjectProviderTranscriptFoldsFailedChunkAttemptsAfterCommit(t *testing.T) {
	const writeID = "write-failed-chunk"
	failedContent := strings.Repeat("oversized secret chunk ", 20)
	validContent := "final content"
	history := []Message{
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "begin_failed_chunk",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "reports/failed-chunk.md", "mode": "create"},
		}}},
		{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "begin_failed_chunk",
			Content:   "begin ok",
			ChunkedWrite: &chunkedwrite.Event{
				Kind:    chunkedwrite.EventBegin,
				WriteID: writeID,
				Path:    "reports/failed-chunk.md",
				Mode:    chunkedwrite.ModeCreate,
			},
		}}},
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "chunk_failed",
			ToolName:  "write_chunk",
			Input:     map[string]any{"write_id": writeID, "index": 0, "content": failedContent},
		}}},
		{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "chunk_failed",
			Content:   "write_chunk: content exceeds max chunk limits",
			IsError:   true,
		}}},
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "chunk_valid",
			ToolName:  "write_chunk",
			Input:     map[string]any{"write_id": writeID, "index": 0, "content": validContent},
		}}},
		{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "chunk_valid",
			Content:   "chunk accepted",
			ChunkedWrite: &chunkedwrite.Event{
				Kind:    chunkedwrite.EventChunk,
				WriteID: writeID,
				Index:   0,
				Bytes:   len(validContent),
				Chars:   len(validContent),
				Chunks:  1,
			},
		}}},
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "commit_failed_chunk",
			ToolName:  "write_commit",
			Input:     map[string]any{"write_id": writeID, "expected_chunks": 1},
		}}},
		{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "commit_failed_chunk",
			Content:   "commit ok",
			ChunkedWrite: &chunkedwrite.Event{
				Kind:    chunkedwrite.EventCommit,
				WriteID: writeID,
				Path:    "reports/failed-chunk.md",
				Bytes:   len(validContent),
				Chars:   len(validContent),
				Chunks:  1,
				SHA256:  "final-hash",
			},
		}}},
	}

	projected := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	text := providerProjectionDebugString(projected)
	if !strings.Contains(text, "Chunked write provider replay summary: committed") {
		t.Fatalf("projected history missing committed summary:\n%s", text)
	}
	if strings.Contains(text, failedContent) || strings.Contains(text, validContent) {
		t.Fatalf("committed projection should fold failed and valid chunk content:\n%s", text)
	}
	if got := providerProjectionToolUseNames(projected); len(got) != 0 {
		t.Fatalf("committed projection should omit all chunked write tool calls, got %+v", got)
	}
}

func TestProjectProviderTranscriptDoesNotParseLegacyChunkedWriteResultText(t *testing.T) {
	const writeID = "legacy-write"
	history := []Message{
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "begin_legacy",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "reports/legacy.md", "mode": "create"},
		}}},
		{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "begin_legacy",
			Content:   "write_begin: write_id=" + writeID + " path=reports/legacy.md mode=create",
		}}},
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "commit_legacy",
			ToolName:  "write_commit",
			Input:     map[string]any{"write_id": writeID, "expected_chunks": 0},
		}}},
		{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "commit_legacy",
			Content:   "write_commit: write_id=legacy-write path=reports/legacy.md bytes=0 chars=0 chunks=0 sha256=legacy",
		}}},
	}

	projected := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	text := providerProjectionDebugString(projected)
	if strings.Contains(text, "Chunked write provider replay summary") {
		t.Fatalf("legacy text-only transcript should not be parsed as lifecycle state:\n%s", text)
	}
	if got := providerProjectionToolUseNames(projected); len(got) != 2 {
		t.Fatalf("legacy tool calls should remain visible, got %+v", got)
	}
}

func TestProjectProviderTranscriptFoldsOnlyOldActiveChunkedWriteChunks(t *testing.T) {
	const writeID = "write-active"
	history := []Message{
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "begin_1",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "drafts/live.md", "mode": "overwrite"},
		}}},
		{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "begin_1",
			Content:   "write_begin: write_id=" + writeID + " path=drafts/live.md mode=overwrite max_chunk_bytes=4000 max_chunk_chars=2000 recommended_chunk_bytes=4000 recommended_chunk_chars=2000",
			ChunkedWrite: &chunkedwrite.Event{
				Kind:    chunkedwrite.EventBegin,
				WriteID: writeID,
				Path:    "drafts/live.md",
				Mode:    chunkedwrite.ModeOverwrite,
			},
		}}},
	}
	chunks := make([]string, 0, providerWriteChunkRecentReplayCount+2)
	for i := 0; i < providerWriteChunkRecentReplayCount+2; i++ {
		chunk := strings.Repeat(fmt.Sprintf("chunk-%d ", i), 10)
		chunks = append(chunks, chunk)
		toolUseID := fmt.Sprintf("chunk_%d", i)
		history = append(history,
			Message{Role: RoleAssistant, Blocks: []Block{{
				Type:      BlockToolUse,
				ToolUseID: toolUseID,
				ToolName:  "write_chunk",
				Input:     map[string]any{"write_id": writeID, "index": i, "content": chunk},
			}}},
			Message{Role: RoleUser, Blocks: []Block{{
				Type:      BlockToolResult,
				ToolUseID: toolUseID,
				Content:   fmt.Sprintf("write_chunk: write_id=%s index=%d bytes=%d chars=%d sha256=hash-%d chunks=%d duplicate=false", writeID, i, len(chunk), len(chunk), i, i+1),
				ChunkedWrite: &chunkedwrite.Event{
					Kind:    chunkedwrite.EventChunk,
					WriteID: writeID,
					Index:   i,
					Bytes:   len(chunk),
					Chars:   len(chunk),
					SHA256:  fmt.Sprintf("hash-%d", i),
					Chunks:  i + 1,
				},
			}}},
		)
	}

	projected := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	text := providerProjectionDebugString(projected)
	if !strings.Contains(text, "Chunked write provider replay summary: active") {
		t.Fatalf("projected history missing active summary:\n%s", text)
	}
	if !strings.Contains(text, "folded_chunks=2") || !strings.Contains(text, "next_index=2") {
		t.Fatalf("active summary missing fold metadata:\n%s", text)
	}
	for _, oldChunk := range chunks[:2] {
		if strings.Contains(text, oldChunk) {
			t.Fatalf("active projection should fold old chunk content %q:\n%s", oldChunk, text)
		}
	}
	for _, recentChunk := range chunks[2:] {
		if !strings.Contains(text, recentChunk) {
			t.Fatalf("active projection should keep recent chunk content %q:\n%s", recentChunk, text)
		}
	}
	if got := providerProjectionToolUseNames(projected); strings.Count(strings.Join(got, ","), "write_chunk") != providerWriteChunkRecentReplayCount {
		t.Fatalf("active projection should keep %d recent write_chunk calls, got %+v", providerWriteChunkRecentReplayCount, got)
	}
	if !strings.Contains(text, "write_begin") || !strings.Contains(text, writeID) {
		t.Fatalf("active projection should keep begin context and write_id:\n%s", text)
	}
	if strings.Contains(text, "content_omitted") {
		t.Fatalf("active projection should not use fake tool arguments:\n%s", text)
	}
}

func TestProjectProviderTranscriptDoesNotFoldUnresolvedChunkedWriteCommit(t *testing.T) {
	const writeID = "write-unresolved"
	chunks := []string{
		strings.Repeat("alpha ", 20),
		strings.Repeat("beta ", 20),
	}
	history := []Message{
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "begin_1",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "drafts/unresolved.md", "mode": "overwrite"},
		}}},
		{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "begin_1",
			Content:   "write_begin: write_id=" + writeID + " path=drafts/unresolved.md mode=overwrite max_chunk_bytes=4000 max_chunk_chars=2000 recommended_chunk_bytes=4000 recommended_chunk_chars=2000",
			ChunkedWrite: &chunkedwrite.Event{
				Kind:    chunkedwrite.EventBegin,
				WriteID: writeID,
				Path:    "drafts/unresolved.md",
				Mode:    chunkedwrite.ModeOverwrite,
			},
		}}},
	}
	for i, chunk := range chunks {
		toolUseID := fmt.Sprintf("chunk_%d", i)
		history = append(history,
			Message{Role: RoleAssistant, Blocks: []Block{{
				Type:      BlockToolUse,
				ToolUseID: toolUseID,
				ToolName:  "write_chunk",
				Input:     map[string]any{"write_id": writeID, "index": i, "content": chunk},
			}}},
			Message{Role: RoleUser, Blocks: []Block{{
				Type:      BlockToolResult,
				ToolUseID: toolUseID,
				Content:   fmt.Sprintf("write_chunk: write_id=%s index=%d bytes=%d chars=%d sha256=hash-%d chunks=%d duplicate=false", writeID, i, len(chunk), len(chunk), i, i+1),
				ChunkedWrite: &chunkedwrite.Event{
					Kind:    chunkedwrite.EventChunk,
					WriteID: writeID,
					Index:   i,
					Bytes:   len(chunk),
					Chars:   len(chunk),
					SHA256:  fmt.Sprintf("hash-%d", i),
					Chunks:  i + 1,
				},
			}}},
		)
	}
	history = append(history, Message{Role: RoleAssistant, Blocks: []Block{{
		Type:      BlockToolUse,
		ToolUseID: "commit_1",
		ToolName:  "write_commit",
		Input:     map[string]any{"write_id": writeID, "expected_chunks": len(chunks)},
	}}})

	projected := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	text := providerProjectionDebugString(projected)
	if strings.Contains(text, "Chunked write provider replay summary: committed") {
		t.Fatalf("unresolved commit should not be folded as committed:\n%s", text)
	}
	for _, chunk := range chunks {
		if !strings.Contains(text, chunk) {
			t.Fatalf("unresolved commit should keep prior chunk content %q:\n%s", chunk, text)
		}
	}
	if got := providerProjectionToolUseNames(projected); !slices.Contains(got, "write_commit") {
		t.Fatalf("unresolved commit tool call should remain visible, got %+v", got)
	}
}

func TestProjectProviderTranscriptDefersActiveChunkedWriteSummaryUntilToolResultsComplete(t *testing.T) {
	const writeID = "write-batch"
	history := []Message{
		{Role: RoleAssistant, Blocks: []Block{{
			Type:      BlockToolUse,
			ToolUseID: "begin_1",
			ToolName:  "write_begin",
			Input:     map[string]any{"path": "drafts/batch.md", "mode": "overwrite"},
		}}},
		{Role: RoleUser, Blocks: []Block{{
			Type:      BlockToolResult,
			ToolUseID: "begin_1",
			Content:   "write_begin: write_id=" + writeID + " path=drafts/batch.md mode=overwrite max_chunk_bytes=4000 max_chunk_chars=2000 recommended_chunk_bytes=4000 recommended_chunk_chars=2000",
			ChunkedWrite: &chunkedwrite.Event{
				Kind:    chunkedwrite.EventBegin,
				WriteID: writeID,
				Path:    "drafts/batch.md",
				Mode:    chunkedwrite.ModeOverwrite,
			},
		}}},
		{Role: RoleAssistant},
		{Role: RoleUser},
	}
	for i := 0; i < providerWriteChunkRecentReplayCount+2; i++ {
		chunk := strings.Repeat(fmt.Sprintf("batch-%d ", i), 10)
		toolUseID := fmt.Sprintf("chunk_%d", i)
		history[2].Blocks = append(history[2].Blocks, Block{
			Type:      BlockToolUse,
			ToolUseID: toolUseID,
			ToolName:  "write_chunk",
			Input:     map[string]any{"write_id": writeID, "index": i, "content": chunk},
		})
		history[3].Blocks = append(history[3].Blocks, Block{
			Type:      BlockToolResult,
			ToolUseID: toolUseID,
			Content:   fmt.Sprintf("write_chunk: write_id=%s index=%d bytes=%d chars=%d sha256=hash-%d chunks=%d duplicate=false", writeID, i, len(chunk), len(chunk), i, i+1),
			ChunkedWrite: &chunkedwrite.Event{
				Kind:    chunkedwrite.EventChunk,
				WriteID: writeID,
				Index:   i,
				Bytes:   len(chunk),
				Chars:   len(chunk),
				SHA256:  fmt.Sprintf("hash-%d", i),
				Chunks:  i + 1,
			},
		})
	}

	projected := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	if err := ValidateToolTranscript(projected); err != nil {
		t.Fatalf("projected transcript should remain valid after active chunk folding: %v\n%s", err, providerProjectionDebugString(projected))
	}

	var userBlocks []Block
	for _, message := range projected {
		if message.Role == RoleUser {
			userBlocks = message.Blocks
		}
	}
	if len(userBlocks) != providerWriteChunkRecentReplayCount+1 {
		t.Fatalf("projected user blocks = %d, want %d: %+v", len(userBlocks), providerWriteChunkRecentReplayCount+1, userBlocks)
	}
	for i := 0; i < providerWriteChunkRecentReplayCount; i++ {
		if userBlocks[i].Type != BlockToolResult {
			t.Fatalf("retained tool_result %d should precede summary: %+v", i, userBlocks)
		}
	}
	last := userBlocks[len(userBlocks)-1]
	if last.Type != BlockText || !strings.Contains(last.Text, "Chunked write provider replay summary: active") {
		t.Fatalf("active summary should be deferred after retained tool_results: %+v", userBlocks)
	}
}

func providerProjectionDebugString(messages []Message) string {
	var out strings.Builder
	for _, message := range messages {
		fmt.Fprintf(&out, "role=%s\n", message.Role)
		for _, block := range message.Blocks {
			switch block.Type {
			case BlockText:
				fmt.Fprintf(&out, "text=%s\n", block.Text)
			case BlockToolUse:
				fmt.Fprintf(&out, "tool_use=%s id=%s input=%+v\n", block.ToolName, block.ToolUseID, block.Input)
			case BlockToolResult:
				fmt.Fprintf(&out, "tool_result id=%s error=%t content=%s\n", block.ToolUseID, block.IsError, block.Content)
			default:
				fmt.Fprintf(&out, "block=%s text=%s content=%s input=%+v\n", block.Type, block.Text, block.Content, block.Input)
			}
		}
	}
	return out.String()
}

func providerProjectionToolUseNames(messages []Message) []string {
	var out []string
	for _, message := range messages {
		for _, block := range message.Blocks {
			if block.Type == BlockToolUse {
				out = append(out, block.ToolName)
			}
		}
	}
	return out
}

func TestProviderIndexRangeDoesNotCollapseDuplicateIndices(t *testing.T) {
	if got := providerIndexRange([]int{0, 2, 2}); got != "0,2,2" {
		t.Fatalf("providerIndexRange duplicate indices = %q, want 0,2,2", got)
	}
}

func TestProjectProviderTranscriptGatesToolsAndReasoning(t *testing.T) {
	history := []Message{
		{
			Role: RoleAssistant,
			Blocks: []Block{
				{Type: BlockText, Text: "thinking result"},
				{Type: BlockReasoning, Text: "hidden", Signature: "rs_1"},
				{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "read", Input: map[string]any{"path": "x"}},
			},
		},
		{
			Role: RoleUser,
			Blocks: []Block{
				{Type: BlockText, Text: "continue"},
				{Type: BlockToolResult, ToolUseID: "call_1", Content: "file"},
			},
		},
	}

	noTools := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{ReasoningReplay: true},
	}, providerProjectionOptions{})
	if len(noTools[0].Blocks) != 2 || noTools[0].Blocks[1].Type != BlockReasoning {
		t.Fatalf("assistant blocks without tools = %+v", noTools[0].Blocks)
	}
	if len(noTools[1].Blocks) != 1 || noTools[1].Blocks[0].Type != BlockText {
		t.Fatalf("user blocks without tools = %+v", noTools[1].Blocks)
	}

	noReasoning := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	if len(noReasoning[0].Blocks) != 2 || noReasoning[0].Blocks[1].Type != BlockToolUse {
		t.Fatalf("assistant blocks without reasoning = %+v", noReasoning[0].Blocks)
	}
	if len(noReasoning[1].Blocks) != 2 || noReasoning[1].Blocks[1].Type != BlockToolResult {
		t.Fatalf("user blocks with tools = %+v", noReasoning[1].Blocks)
	}

	omitReasoning := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true, ReasoningReplay: true},
	}, providerProjectionOptions{OmitReasoning: true})
	for _, b := range omitReasoning[0].Blocks {
		if b.Type == BlockReasoning {
			t.Fatalf("reasoning should be omitted for codex-style projection: %+v", omitReasoning[0].Blocks)
		}
	}

	if len(history[0].Blocks) != 3 || history[0].Blocks[1].Type != BlockReasoning {
		t.Fatalf("projection mutated input history: %+v", history[0].Blocks)
	}
}

func TestProjectProviderTranscriptGatesVision(t *testing.T) {
	media := &MediaRef{ArtifactPath: "image.png", MediaType: "image/png", SHA256: "sha", OriginalBytes: 12}
	history := []Message{
		{Role: RoleUser, Blocks: []Block{{Type: BlockImage, Media: media}}},
		{Role: RoleAssistant, Blocks: []Block{{Type: BlockToolUse, ToolUseID: "call_1", ToolName: "render_chart", Input: map[string]any{"id": "x"}}}},
		{Role: RoleUser, Blocks: []Block{{Type: BlockToolResult, ToolUseID: "call_1", ToolName: "render_chart", Media: media}}},
	}

	noVision := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true},
	}, providerProjectionOptions{})
	if noVision[0].Blocks[0].Type != BlockText || !strings.Contains(noVision[0].Blocks[0].Text, "image.png") {
		t.Fatalf("image without vision = %+v", noVision[0].Blocks[0])
	}
	if noVision[2].Blocks[0].Type != BlockToolResult || noVision[2].Blocks[0].Media != nil || !strings.Contains(noVision[2].Blocks[0].Content, "tool_result_image") {
		t.Fatalf("tool result image without vision = %+v", noVision[2].Blocks[0])
	}

	withVision := projectProviderTranscript(history, ProviderProfile{
		Capabilities: ProviderCapabilities{Tools: true, Vision: true},
	}, providerProjectionOptions{})
	if withVision[0].Blocks[0].Type != BlockImage || withVision[0].Blocks[0].Media == nil {
		t.Fatalf("image with vision = %+v", withVision[0].Blocks[0])
	}
	if withVision[2].Blocks[0].Type != BlockToolResult || withVision[2].Blocks[0].Media == nil {
		t.Fatalf("tool result image with vision = %+v", withVision[2].Blocks[0])
	}
}

func TestProjectProviderTranscriptCompactsAfterFiltering(t *testing.T) {
	history := []Message{
		{
			Role:   RoleUser,
			Blocks: []Block{{Type: BlockText, Text: "first"}},
		},
		{
			Role: RoleAssistant,
			Blocks: []Block{{
				Type:      BlockToolUse,
				ToolUseID: "call_1",
				ToolName:  "read",
				Input:     map[string]any{"path": "x"},
			}},
		},
		{
			Role:   RoleUser,
			Blocks: []Block{{Type: BlockText, Text: "second"}},
		},
	}

	projected := projectProviderTranscript(history, ProviderProfile{}, providerProjectionOptions{})
	if len(projected) != 1 {
		t.Fatalf("projected message count = %d, want 1: %+v", len(projected), projected)
	}
	if projected[0].Role != RoleUser {
		t.Fatalf("projected role = %q, want user", projected[0].Role)
	}
	if len(projected[0].Blocks) != 2 {
		t.Fatalf("projected user block count = %d, want 2: %+v", len(projected[0].Blocks), projected[0].Blocks)
	}
	if got := []string{projected[0].Blocks[0].Text, projected[0].Blocks[1].Text}; got[0] != "first" || got[1] != "second" {
		t.Fatalf("projected user blocks = %+v, want first/second", projected[0].Blocks)
	}
}

func TestNormalizedFunctionParametersDefaults(t *testing.T) {
	schema := normalizedFunctionParameters(map[string]any{"required": []any{"path", 3}})
	assertObjectWithEmptyProperties(t, schema)
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("required-only schema should stay open, got %+v", schema)
	}
	req := normalizedFunctionRequired(schema)
	if len(req) != 1 || req[0] != "path" {
		t.Fatalf("required = %+v, want [path]", req)
	}
	noArgs := normalizedFunctionParameters(map[string]any{"type": "object"})
	assertEmptyProperties(t, noArgs)
	props := normalizedFunctionProperties(map[string]any{"properties": map[string]any{"path": map[string]any{"type": "string"}}})
	if _, ok := props["path"]; !ok {
		t.Fatalf("properties = %+v, want path", props)
	}
}

func TestNormalizedFunctionParametersKeepsExplicitOpenEmptyObject(t *testing.T) {
	schema := normalizedFunctionParameters(map[string]any{"type": "object", "properties": map[string]any{}})
	assertObjectWithEmptyProperties(t, schema)
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("explicit empty properties schema should stay open, got %+v", schema)
	}
}

func TestNormalizedFunctionParametersPreservesExplicitAdditionalProperties(t *testing.T) {
	schema := normalizedFunctionParameters(map[string]any{"type": "object", "additionalProperties": true})
	assertObjectWithEmptyProperties(t, schema)
	if schema["additionalProperties"] != true {
		t.Fatalf("additionalProperties = %+v, want true", schema["additionalProperties"])
	}
}

func TestNormalizedFunctionParametersFlattensComposedObjectProperties(t *testing.T) {
	input := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"source": map[string]any{
				"type": "object",
				"oneOf": []map[string]any{
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"type":    map[string]any{"type": "string", "enum": []any{"command"}},
							"command": map[string]any{"type": "string"},
							"filters": map[string]any{
								"type": "array",
								"items": map[string]any{
									"type":                 "object",
									"additionalProperties": false,
									"properties": map[string]any{
										"contains": map[string]any{"type": "string"},
										"regex":    map[string]any{"type": "string"},
									},
									"oneOf": []map[string]any{
										map[string]any{"required": []any{"contains"}},
										map[string]any{"required": []any{"regex"}},
									},
								},
							},
						},
					},
					map[string]any{
						"type": "object",
						"properties": map[string]any{
							"type":     map[string]any{"type": "string", "enum": []any{"schedule"}},
							"interval": map[string]any{"type": "object"},
						},
						"oneOf": []map[string]any{
							map[string]any{"required": []any{"interval"}},
						},
					},
				},
			},
		},
	}

	normalized := normalizedFunctionParameters(input)
	source := schemaValueMap(t, schemaValueMap(t, normalized["properties"])["source"])
	if _, ok := source["oneOf"]; ok {
		t.Fatalf("source oneOf should be flattened for provider schemas: %+v", source)
	}
	sourceProps := schemaValueMap(t, source["properties"])
	for _, want := range []string{"type", "command", "filters", "interval"} {
		if _, ok := sourceProps[want]; !ok {
			t.Fatalf("source properties missing %q: %+v", want, sourceProps)
		}
	}
	typeEnum, ok := schemaValueMap(t, sourceProps["type"])["enum"].([]any)
	if !ok || len(typeEnum) != 2 || typeEnum[0] != "command" || typeEnum[1] != "schedule" {
		t.Fatalf("source type enum = %#v, want command and schedule", schemaValueMap(t, sourceProps["type"])["enum"])
	}
	filterItems := schemaValueMap(t, schemaValueMap(t, sourceProps["filters"])["items"])
	if filterItems["type"] != "object" {
		t.Fatalf("filter item type = %v, want object in %+v", filterItems["type"], filterItems)
	}
	if _, ok := filterItems["oneOf"]; ok {
		t.Fatalf("filter item oneOf should be dropped for provider schemas: %+v", filterItems)
	}
	filterProps := schemaValueMap(t, filterItems["properties"])
	for _, want := range []string{"contains", "regex"} {
		if _, ok := filterProps[want]; !ok {
			t.Fatalf("filter properties missing %q: %+v", want, filterProps)
		}
	}
	originalSource := schemaValueMap(t, schemaValueMap(t, input["properties"])["source"])
	if _, ok := originalSource["oneOf"]; !ok {
		t.Fatalf("normalization mutated input schema: %+v", input)
	}
}

func TestNormalizedFunctionParametersKeepsRefSchemasOpen(t *testing.T) {
	schema := normalizedFunctionParameters(map[string]any{
		"type": "object",
		"$ref": "#/$defs/query",
		"$defs": map[string]any{
			"query": map[string]any{
				"type":       "object",
				"properties": map[string]any{"query": map[string]any{"type": "string"}},
				"required":   []any{"query"},
			},
		},
	})
	assertObjectWithEmptyProperties(t, schema)
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("ref schema should stay open, got %+v", schema)
	}
}

func TestNormalizedFunctionParametersKeepsPropertyCountSchemasOpen(t *testing.T) {
	schema := normalizedFunctionParameters(map[string]any{"type": "object", "minProperties": 1})
	assertObjectWithEmptyProperties(t, schema)
	if _, ok := schema["additionalProperties"]; ok {
		t.Fatalf("property-count schema should stay open, got %+v", schema)
	}
}

func TestParseToolArguments(t *testing.T) {
	input := parseToolArguments(`{"path":"x","content":"hello"}`)
	if input["path"] != "x" || input["content"] != "hello" {
		t.Fatalf("input = %+v, want parsed object", input)
	}

	doubleEncoded := parseToolArguments(`"{\"path\":\"x\",\"content\":\"hello\"}"`)
	if doubleEncoded["path"] != "x" || doubleEncoded["content"] != "hello" {
		t.Fatalf("doubleEncoded = %+v, want parsed object", doubleEncoded)
	}

	raw := parseToolArguments(`{"path":`)
	if raw["_raw_arguments"] != `{"path":` {
		t.Fatalf("raw = %+v, want preserved raw arguments", raw)
	}
}
