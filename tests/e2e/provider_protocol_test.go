package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/thread"
)

type liveSendResult struct {
	AgentID   string
	ThreadID  string
	ThreadDir string
	Stdout    string
	Stderr    string
}

func sendAndWait(t *testing.T, bin, home, work string, args ...string) liveSendResult {
	t.Helper()
	commandArgs := append([]string{"send", "--wait", "--json"}, args...)
	deadline := time.Now().Add(5 * time.Second)
	var stdout, stderr string
	var err error
	for {
		stdout, stderr, err = runAgentStateCommand(bin, home, work, commandArgs...)
		if err == nil || !strings.Contains(stderr, "HTTP 409") || !strings.Contains(stderr, "Thread busy") || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("juex %s: %v\nstdout:\n%s\nstderr:\n%s", strings.Join(commandArgs, " "), err, stdout, stderr)
	}
	result := liveSendResult{Stdout: stdout, Stderr: stderr}
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var receipt struct {
			AgentID  string `json:"agent_id"`
			ThreadID string `json:"thread_id"`
			InputID  string `json:"input_id"`
		}
		if json.Unmarshal([]byte(line), &receipt) == nil && receipt.InputID != "" {
			result.AgentID = receipt.AgentID
			result.ThreadID = receipt.ThreadID
			break
		}
	}
	if result.AgentID == "" || result.ThreadID == "" {
		t.Fatalf("send receipt missing Agent or Thread identity:\n%s", stdout)
	}
	if !strings.Contains(stdout, `"type":"input.terminal"`) || !strings.Contains(stdout, `"state":"succeeded"`) {
		t.Fatalf("send did not reach terminal success:\n%s", stdout)
	}
	result.ThreadDir = filepath.Join(home, "agents", result.AgentID, "threads", result.ThreadID)
	return result
}

func sendAndWaitFailure(t *testing.T, bin, home, work string, args ...string) liveSendResult {
	t.Helper()
	commandArgs := append([]string{"send", "--wait", "--json"}, args...)
	stdout, stderr, err := runAgentStateCommand(bin, home, work, commandArgs...)
	if err == nil {
		t.Fatalf("juex %s unexpectedly succeeded:\n%s", strings.Join(commandArgs, " "), stdout)
	}
	result := liveSendResult{Stdout: stdout, Stderr: stderr}
	for _, line := range strings.Split(stdout, "\n") {
		var receipt struct {
			AgentID  string `json:"agent_id"`
			ThreadID string `json:"thread_id"`
			InputID  string `json:"input_id"`
		}
		if json.Unmarshal([]byte(line), &receipt) == nil && receipt.InputID != "" {
			result.AgentID = receipt.AgentID
			result.ThreadID = receipt.ThreadID
			break
		}
	}
	if result.AgentID == "" || result.ThreadID == "" {
		t.Fatalf("failed send omitted durable receipt:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	result.ThreadDir = filepath.Join(home, "agents", result.AgentID, "threads", result.ThreadID)
	return result
}

func stopLiveAgent(t *testing.T, bin, home, work string) {
	t.Helper()
	resolution, err := agentstate.ResolveExisting(agentstate.Options{HomeDir: home, WorkDir: work})
	if err != nil {
		return
	}
	stdout, stderr, err := runJuexHomeCommand(bin, home, "fleet", "stop", resolution.Agent.ID)
	if err != nil {
		t.Errorf("stop live Agent %s: %v\nstdout:\n%s\nstderr:\n%s", resolution.Agent.ID, err, stdout, stderr)
	}
}

func readThreadMessages(t *testing.T, threadDir string) []llm.Message {
	t.Helper()
	target, err := thread.Load(threadDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = target.Close() }()
	page, err := target.Timeline("", 500)
	if err != nil {
		t.Fatal(err)
	}
	var messages []llm.Message
	for _, item := range page.Items {
		if item.Message != nil {
			messages = append(messages, *item.Message)
		}
	}
	if page.HasMoreBefore {
		t.Fatal("test Thread timeline exceeded one 500-item page")
	}
	return messages
}

func threadJournalText(t *testing.T, threadDir string) string {
	t.Helper()
	journals, err := thread.InspectGenerationJournals(threadDir)
	if err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	for _, journal := range journals {
		data, err := os.ReadFile(journal.Path)
		if err != nil {
			t.Fatal(err)
		}
		content.Write(data)
	}
	return content.String()
}

func currentGenerationJournalPath(t *testing.T, threadDir string) string {
	t.Helper()
	journals, err := thread.InspectGenerationJournals(threadDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(journals) == 0 {
		t.Fatal("Thread has no registered Generation Journal")
	}
	return journals[len(journals)-1].Path
}

func TestLiveBinary_ProviderProtocolAndThinkingMatrix(t *testing.T) {
	bin := buildJuex(t)

	cases := []struct {
		name                  string
		modelRef              string
		providerYAML          string
		wantPathSuffix        string
		wantReasoningEffort   string
		wantNoReasoningEffort bool
		wantReasoningBlocks   int
		responseBody          string
	}{
		{
			name:                "openai responses sends reasoning effort",
			modelRef:            "openai:gpt-test",
			wantPathSuffix:      "/responses",
			wantReasoningEffort: "high",
			wantReasoningBlocks: 2,
			providerYAML: `  - id: openai
    protocol: openai/responses
    base_url: BASE_URL
    api_key: k
    capabilities:
      streaming: false
    models:
      - id: gpt-test
        thinking_effort: high
`,
			responseBody: `{
  "id": "resp_1",
  "object": "response",
  "model": "gpt-test",
  "status": "completed",
  "output": [
	{
	  "type": "reasoning",
	  "id": "rs_e2e_1",
	  "summary": [{"type": "summary_text", "text": "first summary"}],
	  "encrypted_content": "encrypted-e2e-1"
	},
	{
	  "type": "reasoning",
	  "id": "rs_e2e_2",
	  "summary": [{"type": "summary_text", "text": "second summary"}],
	  "encrypted_content": "encrypted-e2e-2"
	},
    {
      "type": "message",
      "id": "msg_1",
      "role": "assistant",
      "status": "completed",
      "content": [{"type": "output_text", "text": "responses-ok", "annotations": []}]
    }
  ],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`,
		},
		{
			name:                "custom openai chat defaults reasoning effort on",
			modelRef:            "local-chat:chat-test",
			wantPathSuffix:      "/chat/completions",
			wantReasoningEffort: "xhigh",
			providerYAML: `  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    capabilities:
      streaming: false
    models:
      - id: chat-test
        thinking_effort: xhigh
`,
			responseBody: chatCompletionResponse("chat-ok"),
		},
		{
			name:                "deepseek preset uses openai chat reasoning effort",
			modelRef:            "deepseek:deepseek-v4-pro",
			wantPathSuffix:      "/chat/completions",
			wantReasoningEffort: "max",
			providerYAML: `  - id: deepseek
    base_url: BASE_URL
    api_key: k
    capabilities:
      streaming: false
    models:
      - id: deepseek-v4-pro
        thinking_effort: max
`,
			responseBody: chatCompletionResponse("deepseek-ok"),
		},
		{
			name:                  "capability can disable reasoning effort",
			modelRef:              "local-chat:chat-test",
			wantPathSuffix:        "/chat/completions",
			wantNoReasoningEffort: true,
			providerYAML: `  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    capabilities:
      reasoning_effort: false
      streaming: false
    models:
      - id: chat-test
        thinking_effort: high
`,
			responseBody: chatCompletionResponse("disabled-ok"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan capturedProviderRequest, 1)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				requests <- capturedProviderRequest{path: r.URL.Path, body: body}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.responseBody))
			}))
			defer srv.Close()

			work := t.TempDir()
			configPath := filepath.Join(work, ".juex", "juex.yaml")
			body := "models: [" + tc.modelRef + "]\nproviders:\n" +
				strings.ReplaceAll(tc.providerYAML, "BASE_URL", srv.URL)
			if err := writeText(configPath, body); err != nil {
				t.Fatal(err)
			}

			home := t.TempDir()
			t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
			result := sendAndWait(t, bin, home, work, "hello")
			var captured capturedProviderRequest
			select {
			case captured = <-requests:
			default:
				t.Fatal("fake provider did not receive a request")
			}
			if !strings.HasSuffix(captured.path, tc.wantPathSuffix) {
				t.Fatalf("request path = %q, want suffix %q", captured.path, tc.wantPathSuffix)
			}
			if model, _ := captured.body["model"].(string); model == "" {
				t.Fatalf("request body missing model: %+v", captured.body)
			}
			if tc.wantNoReasoningEffort {
				if _, ok := captured.body["reasoning_effort"]; ok {
					t.Fatalf("reasoning_effort should be omitted when disabled: %+v", captured.body)
				}
				if _, ok := captured.body["reasoning"]; ok {
					t.Fatalf("reasoning should be omitted when disabled: %+v", captured.body)
				}
				return
			}
			if tc.wantPathSuffix == "/responses" {
				reasoning, ok := captured.body["reasoning"].(map[string]any)
				if !ok || reasoning["effort"] != tc.wantReasoningEffort || reasoning["summary"] != "auto" {
					t.Fatalf("responses reasoning = %+v, want effort %q; body=%+v", reasoning, tc.wantReasoningEffort, captured.body)
				}
			} else if got := captured.body["reasoning_effort"]; got != tc.wantReasoningEffort {
				t.Fatalf("reasoning_effort = %v, want %q; body=%+v", got, tc.wantReasoningEffort, captured.body)
			}
			if tc.wantReasoningBlocks > 0 {
				assertLiveResponsesReasoningHistory(t, result.ThreadDir, tc.wantReasoningBlocks)
			}
		})
	}
}

func assertLiveResponsesReasoningHistory(
	t *testing.T,
	threadDir string,
	want int,
) {
	t.Helper()
	var reasoning []llm.Block
	for _, message := range readThreadMessages(t, threadDir) {
		if message.Role != llm.RoleAssistant {
			continue
		}
		for _, block := range message.Blocks {
			if block.Type == llm.BlockReasoning {
				reasoning = append(reasoning, block)
			}
		}
	}
	if len(reasoning) != want {
		t.Fatalf("Thread journal reasoning blocks = %+v, want %d independent blocks", reasoning, want)
	}
	if reasoning[0].Signature != "rs_e2e_1" || reasoning[0].Content != "encrypted-e2e-1" || reasoning[0].Text != "first summary" || !reasoning[0].Redacted {
		t.Fatalf("first Thread journal reasoning block = %+v", reasoning[0])
	}
	if reasoning[1].Signature != "rs_e2e_2" || reasoning[1].Content != "encrypted-e2e-2" || reasoning[1].Text != "second summary" || !reasoning[1].Redacted {
		t.Fatalf("second Thread journal reasoning block = %+v", reasoning[1])
	}
}

func TestLiveBinary_OpenAIChatStreamsByDefault(t *testing.T) {
	bin := buildJuex(t)
	requests := make(chan capturedProviderRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- capturedProviderRequest{path: r.URL.Path, body: body}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, `data: {"id":"cmpl_e2e","object":"chat.completion.chunk","created":1,"model":"chat-test","choices":[{"index":0,"delta":{"role":"assistant","content":"stream-"},"finish_reason":null}]}`+"\n\n")
		_, _ = fmt.Fprint(w, `data: {"id":"cmpl_e2e","object":"chat.completion.chunk","created":1,"model":"chat-test","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`+"\n\n")
		_, _ = fmt.Fprint(w, `data: {"id":"cmpl_e2e","object":"chat.completion.chunk","created":1,"model":"chat-test","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`+"\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	configBody := "models: [local-chat:chat-test]\nproviders:\n" + strings.ReplaceAll(`  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    models:
      - id: chat-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, configBody); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	result := sendAndWait(t, bin, home, work, "hello")
	if !strings.Contains(threadJournalText(t, result.ThreadDir), "stream-ok") {
		t.Fatalf("Thread journal does not contain streamed assistant output")
	}
	request := <-requests
	if request.path != "/chat/completions" || request.body["stream"] != true {
		t.Fatalf("stream request = %+v", request)
	}
	streamOptions, _ := request.body["stream_options"].(map[string]any)
	if streamOptions["include_usage"] != true {
		t.Fatalf("stream_options = %+v, want include_usage=true", streamOptions)
	}
}

func TestLiveBinary_ThreadProjectionIndexAndMainSelection(t *testing.T) {
	bin := buildJuex(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionResponse("thread-ok")))
	}))
	defer provider.Close()

	work := t.TempDir()
	configBody := fmt.Sprintf(`models: [local:test-model]
providers:
  - id: local
    protocol: openai/chat
    base_url: %s
    api_key: test-key
    capabilities:
      streaming: false
    models:
      - id: test-model
`, provider.URL)
	if err := writeText(filepath.Join(work, ".juex", "juex.yaml"), configBody); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	first := sendAndWait(t, bin, home, work, "first turn")
	second := sendAndWait(t, bin, home, work, "second turn")
	if first.ThreadID != thread.MainID || second.ThreadID != first.ThreadID || second.ThreadDir != first.ThreadDir {
		t.Fatalf("default sends did not target stable Main Thread: first=%+v second=%+v", first, second)
	}
	projectionBytes, err := os.ReadFile(filepath.Join(first.ThreadDir, "thread.json"))
	if err != nil {
		t.Fatal(err)
	}
	var projection thread.Projection
	if err := json.Unmarshal(projectionBytes, &projection); err != nil {
		t.Fatal(err)
	}
	if projection.ThreadID != thread.MainID || projection.Alias != thread.MainAlias ||
		projection.CreatedAt.IsZero() || projection.LastActivityAt.Before(projection.CreatedAt.Time) ||
		projection.Counts.TurnCount != 2 || projection.Revision == 0 {
		t.Fatalf("Main Thread projection = %+v", projection)
	}
	indexBytes, err := os.ReadFile(filepath.Join(home, "agents", first.AgentID, "threads.index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var index thread.Index
	if err := json.Unmarshal(indexBytes, &index); err != nil {
		t.Fatal(err)
	}
	if len(index.Threads) != 1 || index.Threads[0].ThreadID != thread.MainID ||
		index.Threads[0].TurnCount != 2 || index.Threads[0].ThreadRevision != projection.Revision {
		t.Fatalf("Thread index = %+v; projection=%+v", index, projection)
	}
}

func TestLiveBinary_ModelFallbackPersistsNoticeAndServingModel(t *testing.T) {
	bin := buildJuex(t)
	var primaryCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if primaryCalls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(chatCompletionResponse("primary-ok")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key","type":"authentication_error","code":"invalid_api_key"}}`))
	}))
	defer primary.Close()
	var backupCalls atomic.Int32
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionResponse("backup-ok")))
	}))
	defer backup.Close()

	work := t.TempDir()
	configBody := fmt.Sprintf(`models:
  - primary:primary-model
  - backup:backup-model
runtime:
  notify_model_changes: true
providers:
  - id: primary
    protocol: openai/chat
    base_url: %s
    api_key: primary-key
    capabilities:
      streaming: false
    models:
      - id: primary-model
  - id: backup
    protocol: openai/chat
    base_url: %s
    api_key: backup-key
    capabilities:
      streaming: false
    models:
      - id: backup-model
`, primary.URL, backup.URL)
	if err := writeText(filepath.Join(work, ".juex", "juex.yaml"), configBody); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	first := sendAndWait(t, bin, home, work, "first turn")
	second := sendAndWait(t, bin, home, work, "second turn")
	if first.ThreadID != thread.MainID || second.ThreadID != first.ThreadID {
		t.Fatalf("fallback sends changed Main Thread: first=%+v second=%+v", first, second)
	}
	messages := readThreadMessages(t, second.ThreadDir)
	if len(messages) < 2 {
		t.Fatalf("Thread messages = %d", len(messages))
	}
	notice, assistant := messages[len(messages)-2], messages[len(messages)-1]
	if notice.Kind != llm.MessageKindModelChange || assistant.Model != "backup:backup-model" {
		t.Fatalf("fallback tail = %+v / %+v", notice, assistant)
	}
	eventsText := threadJournalText(t, second.ThreadDir)
	if !strings.Contains(eventsText, `"type":"llm.fallback"`) || backupCalls.Load() != 1 {
		t.Fatalf("fallback event/requests missing: backup=%d events=%s", backupCalls.Load(), eventsText)
	}
}

func TestLiveBinary_ModelFallbackDoesNotNotifyByDefault(t *testing.T) {
	bin := buildJuex(t)
	var primaryCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if primaryCalls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(chatCompletionResponse("primary-ok")))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid key","type":"authentication_error","code":"invalid_api_key"}}`))
	}))
	defer primary.Close()
	backup := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionResponse("backup-ok")))
	}))
	defer backup.Close()

	work := t.TempDir()
	configBody := fmt.Sprintf(`models:
  - primary:primary-model
  - backup:backup-model
providers:
  - id: primary
    protocol: openai/chat
    base_url: %s
    api_key: primary-key
    capabilities:
      streaming: false
    models:
      - id: primary-model
  - id: backup
    protocol: openai/chat
    base_url: %s
    api_key: backup-key
    capabilities:
      streaming: false
    models:
      - id: backup-model
`, primary.URL, backup.URL)
	if err := writeText(filepath.Join(work, ".juex", "juex.yaml"), configBody); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	first := sendAndWait(t, bin, home, work, "first turn")
	second := sendAndWait(t, bin, home, work, "second turn")
	if second.ThreadID != first.ThreadID {
		t.Fatalf("fallback sends changed Main Thread: first=%+v second=%+v", first, second)
	}
	messages := readThreadMessages(t, second.ThreadDir)
	var tail llm.Message
	for _, message := range messages {
		if message.Kind == llm.MessageKindModelChange {
			t.Fatalf("default configuration persisted model-change notice: %+v", message)
		}
		tail = message
	}
	if tail.Role != llm.RoleAssistant || tail.Model != "backup:backup-model" {
		t.Fatalf("fallback tail = %+v", tail)
	}
	eventsText := threadJournalText(t, second.ThreadDir)
	if !strings.Contains(eventsText, `"type":"llm.fallback"`) {
		t.Fatalf("fallback event missing: %s", eventsText)
	}
}

func TestLiveBinary_CodexSSERetriesAfterProvisionalOutput(t *testing.T) {
	bin := buildJuex(t)
	var requests atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if requests.Add(1) == 1 {
			_, _ = w.Write([]byte(`data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"partial"}` + "\n\n"))
			w.(http.Flusher).Flush()
			return
		}
		_, _ = w.Write([]byte(`data: {"type":"response.completed","response":{"id":"resp_recovered","model":"gpt-test","status":"completed","output":[{"type":"message","id":"msg_recovered","role":"assistant","status":"completed","content":[{"type":"output_text","text":"recovered","annotations":[]}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"))
	}))
	defer provider.Close()

	work := t.TempDir()
	configBody := fmt.Sprintf(`models: [openai-codex:gpt-test]
providers:
  - id: openai-codex
    base_url: %s
    api_key: codex-test-key
    compat:
      codex_transport: sse
    models:
      - id: gpt-test
`, provider.URL)
	if err := writeText(filepath.Join(work, ".juex", "juex.yaml"), configBody); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	result := sendAndWait(t, bin, home, work, "recover the turn")
	if requests.Load() != 2 {
		t.Fatalf("provider requests=%d, want 2", requests.Load())
	}
	conversation := threadJournalText(t, result.ThreadDir)
	if strings.Contains(conversation, "partial") || !strings.Contains(conversation, "recovered") {
		t.Fatalf("conversation retained provisional output or lost recovery:\n%s", conversation)
	}
	eventLog := conversation
	if !strings.Contains(eventLog, `"type":"llm.retry"`) ||
		!strings.Contains(eventLog, `"type":"llm.responded"`) ||
		strings.Contains(eventLog, `"type":"turn.errored"`) {
		t.Fatalf("retry lifecycle events are incomplete:\n%s", eventLog)
	}
}

func TestLiveBinary_SendResumesWorkerWithoutChangingMain(t *testing.T) {
	bin := buildJuex(t)
	var requestCount atomic.Int32
	var mu sync.Mutex
	var requests []map[string]any
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch requestCount.Add(1) {
		case 1:
			_, _ = w.Write([]byte(chatCompletionResponse("side-first-ok")))
		case 2:
			_, _ = w.Write([]byte(chatCompletionResponse("side-continued-ok")))
		default:
			t.Errorf("unexpected provider request %d", requestCount.Load())
			_, _ = w.Write([]byte(chatCompletionResponse("unexpected")))
		}
	}))
	defer provider.Close()

	work := t.TempDir()
	configBody := fmt.Sprintf(`models: [local-chat:chat-test]
providers:
  - id: local-chat
    protocol: openai/chat
    base_url: %s
    api_key: k
    capabilities:
      streaming: false
    models:
      - id: chat-test
`, provider.URL)
	if err := writeText(filepath.Join(work, ".juex", "juex.yaml"), configBody); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	createdOut, createdErr, err := runAgentStateCommand(bin, home, work, "threads", "create", "--alias", "reviewer")
	if err != nil {
		t.Fatalf("create Worker Thread: %v\nstdout:\n%s\nstderr:\n%s", err, createdOut, createdErr)
	}
	var created thread.Info
	if err := json.Unmarshal([]byte(strings.TrimSpace(createdOut)), &created); err != nil {
		t.Fatalf("decode created Worker: %v\n%s", err, createdOut)
	}
	if !thread.ValidWorkerID(created.ID) || created.ParentThreadID != thread.MainID || created.Alias != "reviewer" {
		t.Fatalf("created Worker = %+v", created)
	}
	first := sendAndWait(t, bin, home, work, "--thread", created.ID, "first worker turn")
	continued := sendAndWait(t, bin, home, work, "--thread", created.ID, "second worker turn")
	if continued.ThreadID != first.ThreadID || continued.ThreadDir != first.ThreadDir {
		t.Fatalf("continued Worker changed identity: first=%+v continued=%+v", first, continued)
	}

	if got := requestCount.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2", got)
	}
	mu.Lock()
	secondRequest, err := json.Marshal(requests[1])
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"first worker turn", "side-first-ok", "second worker turn"} {
		if !bytes.Contains(secondRequest, []byte(want)) {
			t.Fatalf("continued provider request missing %q: %s", want, secondRequest)
		}
	}
	conversation := []byte(threadJournalText(t, continued.ThreadDir))
	for _, want := range []string{"first worker turn", "side-first-ok", "second worker turn", "side-continued-ok"} {
		if !bytes.Contains(conversation, []byte(want)) {
			t.Fatalf("continued Worker journal missing %q:\n%s", want, conversation)
		}
	}
	mainJournal := threadJournalText(t, filepath.Join(home, "agents", first.AgentID, "threads", thread.MainID))
	if strings.Contains(mainJournal, "first worker turn") || strings.Contains(mainJournal, "second worker turn") {
		t.Fatalf("Worker inputs leaked into Main Thread journal:\n%s", mainJournal)
	}
}

type capturedProviderRequest struct {
	path string
	body map[string]any
}

func TestLiveBinary_CLISendAttachmentSendsImageAndPersistsMedia(t *testing.T) {
	bin := buildJuex(t)
	requests := make(chan capturedProviderRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- capturedProviderRequest{path: r.URL.Path, body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionResponse("attachment-ok")))
	}))
	defer srv.Close()

	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	configBody := `models: [local-chat:vision-test]
providers:
  - id: local-chat
    protocol: openai/chat
    base_url: ` + srv.URL + `
    api_key: k
    capabilities:
      vision: true
      streaming: false
    models:
      - id: vision-test
`
	if err := writeText(configPath, configBody); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(sourcePath, webUploadPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	result := sendAndWait(t, bin, home, work, "--attach", sourcePath, "describe this image")
	if !strings.Contains(threadJournalText(t, result.ThreadDir), "attachment-ok") {
		t.Fatalf("Thread journal missing attachment response")
	}

	var captured capturedProviderRequest
	select {
	case captured = <-requests:
	default:
		t.Fatal("fake provider did not receive attachment request")
	}
	if captured.path != "/chat/completions" || !requestHasUserImage(captured.body, "describe this image") {
		t.Fatalf("provider request missing text/image content: path=%q body=%+v", captured.path, captured.body)
	}

	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	var storedRef *llm.MediaRef
	for _, message := range readThreadMessages(t, result.ThreadDir) {
		for _, block := range message.Blocks {
			if block.Type == llm.BlockImage && block.Media != nil {
				storedRef = block.Media
			}
		}
	}
	if storedRef == nil || !strings.Contains(storedRef.ArtifactPath, "/"+result.ThreadID+"/") {
		t.Fatalf("stored media ref = %+v", storedRef)
	}
	agentStateDir := filepath.Join(home, "agents", result.AgentID)
	if _, err := os.Stat(filepath.Join(agentStateDir, "media", filepath.FromSlash(storedRef.ArtifactPath))); err != nil {
		t.Fatalf("persisted media unavailable after source removal: %v", err)
	}
}

func TestLiveBinary_CLISendNonVisionAttachmentWarnsAndProjectsUnavailableText(t *testing.T) {
	bin := buildJuex(t)
	requests := make(chan capturedProviderRequest, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- capturedProviderRequest{path: r.URL.Path, body: body}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(chatCompletionResponse("I cannot view the attached image.")))
	}))
	defer srv.Close()

	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	configBody := `models: [local-chat:text-test]
providers:
  - id: local-chat
    protocol: openai/chat
    base_url: ` + srv.URL + `
    api_key: k
    capabilities:
      streaming: false
    models:
      - id: text-test
`
	if err := writeText(configPath, configBody); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(t.TempDir(), "photo_a.png")
	if err := os.WriteFile(sourcePath, webUploadPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	result := sendAndWait(t, bin, home, work, "--attach", sourcePath, "what color is this image?")
	if !strings.Contains(result.Stdout, "local-chat:text-test") ||
		!strings.Contains(result.Stdout, "providers[].models[].capabilities.vision") {
		t.Fatalf("send receipt warning missing:\n%s", result.Stdout)
	}
	if !strings.Contains(threadJournalText(t, result.ThreadDir), "I cannot view the attached image.") {
		t.Fatalf("Thread journal missing non-vision response")
	}

	captured := <-requests
	if captured.path != "/chat/completions" || !requestUserContentContains(
		captured.body,
		"what color is this image?",
		"cannot view image content",
		"instead of guessing",
	) {
		t.Fatalf("provider request missing unavailable-image guidance: path=%q body=%+v", captured.path, captured.body)
	}
	if requestHasUserImage(captured.body, "what color is this image?") {
		t.Fatalf("non-vision request unexpectedly contained image data: %+v", captured.body)
	}
}

func TestLiveBinary_CLISendExecCommandTool(t *testing.T) {
	bin := buildJuex(t)

	const (
		marker          = "JUEX_CLI_EXEC_E2E"
		extensionEnvKey = "CLI_EXTENSION_DEFAULT"
		extensionName   = "cli-env"
	)
	var requestCount atomic.Int32
	var mu sync.Mutex
	var firstBody map[string]any
	var secondBody map[string]any
	execCommand := `printf '%s:%s\n' ` + marker + ` "$` + extensionEnvKey + `"`
	if runtime.GOOS == "windows" {
		execCommand = `Write-Output ("` + marker + `:" + $env:` + extensionEnvKey + `)`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")

		switch requestCount.Add(1) {
		case 1:
			mu.Lock()
			firstBody = body
			mu.Unlock()
			writeJSON(t, w, chatToolCallResponse("call_exec_cli", "exec_command", map[string]any{
				"cmd": execCommand,
			}))
		case 2:
			mu.Lock()
			secondBody = body
			mu.Unlock()
			writeJSON(t, w, chatCompletionResponseMap("cli exec command complete"))
		default:
			t.Errorf("unexpected provider request %d: %+v", requestCount.Load(), body)
			writeJSON(t, w, chatCompletionResponseMap("unexpected"))
		}
	}))
	defer srv.Close()

	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	body := "models: [local-chat:chat-test]\nextensions:\n  allow: [" + extensionName + "]\nproviders:\n" + strings.ReplaceAll(`  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    capabilities:
      streaming: false
    models:
      - id: chat-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, body); err != nil {
		t.Fatal(err)
	}
	extensionDir := filepath.Join(work, ".juex", "extensions", extensionName)
	if err := writeText(filepath.Join(extensionDir, "juex.extension.json"), `{
  "manifest_version":1,
  "name":"cli-env",
  "version":"1.0.0",
  "agent":{"environment":{"variables":{"CLI_EXTENSION_DEFAULT":"${JUEX_EXT_DATA_DIR}"}}}
}`); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	result := sendAndWait(t, bin, home, work, "run the exec command e2e marker")
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2", got)
	}
	if !strings.Contains(threadJournalText(t, result.ThreadDir), "cli exec command complete") {
		t.Fatalf("Thread journal missing final tool response")
	}

	mu.Lock()
	first := cloneMap(firstBody)
	second := cloneMap(secondBody)
	mu.Unlock()
	if !requestHasTool(first, "exec_command") || !requestHasTool(first, "write_stdin") || !requestHasTool(first, "list_shell_sessions") {
		t.Fatalf("first provider request missing shell tool family: %+v", first["tools"])
	}
	wantToolResultSuffix := filepath.ToSlash(filepath.Join("agents", result.AgentID, "extensions", extensionName))
	toolResult, ok := providerToolResultContent(second, "call_exec_cli")
	if !ok || !strings.Contains(toolResult, marker+":") || !strings.Contains(filepath.ToSlash(toolResult), wantToolResultSuffix) {
		t.Fatalf("second provider request missing Extension-backed exec_command result ending in %q: %+v", wantToolResultSuffix, second["messages"])
	}

	assertThreadExecCommandToolRoundTrip(t, readThreadMessages(t, result.ThreadDir), "call_exec_cli", marker)
	journal := threadJournalText(t, result.ThreadDir)
	for _, want := range []string{`"type":"tool.completed"`, `"type":"finish.attempted"`} {
		if !strings.Contains(journal, want) {
			t.Fatalf("Thread journal missing %q: %s", want, journal)
		}
	}
}

func TestLiveBinary_CLIVerboseCompactsToolBatch(t *testing.T) {
	bin := buildJuex(t)

	var requestCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch requestCount.Add(1) {
		case 1:
			writeJSON(t, w, chatToolCallsResponse([]providerToolCall{
				{ID: "call_read_a", Name: "read", Arguments: map[string]any{"path": "a.txt"}},
				{ID: "call_read_b", Name: "read", Arguments: map[string]any{"path": "b.txt"}},
			}))
		case 2:
			writeJSON(t, w, chatCompletionResponseMap("verbose compact complete"))
		default:
			t.Errorf("unexpected provider request %d: %+v", requestCount.Load(), body)
			writeJSON(t, w, chatCompletionResponseMap("unexpected"))
		}
	}))
	defer srv.Close()

	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	configBody := "models: [local-chat:chat-test]\nproviders:\n" + strings.ReplaceAll(`  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    capabilities:
      streaming: false
    models:
      - id: chat-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, configBody); err != nil {
		t.Fatal(err)
	}
	if err := writeText(filepath.Join(work, "a.txt"), "alpha"); err != nil {
		t.Fatal(err)
	}
	if err := writeText(filepath.Join(work, "b.txt"), "bravo"); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	result := sendAndWait(t, bin, home, work, "read both files")
	journal := threadJournalText(t, result.ThreadDir)
	if !strings.Contains(journal, "verbose compact complete") ||
		!strings.Contains(journal, `"type":"tool.completed"`) {
		t.Fatalf("Thread journal missing batched tool completion:\n%s", journal)
	}
	for _, unwanted := range []string{"\x1b[", "\r"} {
		if strings.Contains(result.Stdout, unwanted) || strings.Contains(result.Stderr, unwanted) {
			t.Fatalf("non-TTY send output contains terminal control artifact %q", unwanted)
		}
	}
}

func TestLiveBinary_ShellYieldIgnoresRuntimeToolTimeout(t *testing.T) {
	bin := buildJuex(t)

	var requestCount atomic.Int32
	var mu sync.Mutex
	var secondBody map[string]any
	var thirdBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")

		switch requestCount.Add(1) {
		case 1:
			writeJSON(t, w, chatToolCallResponse("call_exec_yield", "exec_command", map[string]any{
				"cmd":           slowShellYieldCommand(),
				"yield_time_ms": 1600,
			}))
		case 2:
			mu.Lock()
			secondBody = body
			mu.Unlock()
			sessionID, ok := sessionIDFromProviderToolResult(body, "call_exec_yield")
			if !ok {
				t.Errorf("second request missing running shell session id: %+v", body["messages"])
				writeJSON(t, w, chatCompletionResponseMap("missing session id"))
				return
			}
			writeJSON(t, w, chatToolCallResponse("call_stdin_yield", "write_stdin", map[string]any{
				"session_id":    sessionID,
				"yield_time_ms": 1500,
			}))
		case 3:
			mu.Lock()
			thirdBody = body
			mu.Unlock()
			writeJSON(t, w, chatCompletionResponseMap("yield semantics complete"))
		default:
			t.Errorf("unexpected provider request %d: %+v", requestCount.Load(), body)
			writeJSON(t, w, chatCompletionResponseMap("unexpected"))
		}
	}))
	defer srv.Close()

	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	body := "models: [local-chat:chat-test]\nruntime:\n  tool_timeout: 1s\nproviders:\n" + strings.ReplaceAll(`  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    capabilities:
      streaming: false
    models:
      - id: chat-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, body); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	result := sendAndWait(t, bin, home, work, "run the shell yield timeout e2e")
	if got := requestCount.Load(); got != 3 {
		t.Fatalf("provider requests = %d, want 3", got)
	}
	if !strings.Contains(threadJournalText(t, result.ThreadDir), "yield semantics complete") {
		t.Fatalf("Thread journal missing final shell-yield response")
	}

	mu.Lock()
	second := cloneMap(secondBody)
	third := cloneMap(thirdBody)
	mu.Unlock()
	if !requestHasToolResult(second, "call_exec_yield", "Process running with session ID") {
		t.Fatalf("second provider request missing running exec result: %+v", second["messages"])
	}
	if requestHasToolResult(second, "call_exec_yield", "timed out") {
		t.Fatalf("exec_command result should not be a timeout: %+v", second["messages"])
	}
	if !requestHasToolResult(second, "call_exec_yield", "slow start") &&
		!requestHasToolResult(third, "call_stdin_yield", "slow start") {
		t.Fatalf("provider requests missing initial shell output: second=%+v third=%+v", second["messages"], third["messages"])
	}
	if !requestHasToolResult(third, "call_stdin_yield", "slow done") ||
		!requestHasToolResult(third, "call_stdin_yield", "Process exited with code 0") {
		t.Fatalf("third provider request missing successful poll result: %+v", third["messages"])
	}
	if requestHasToolResult(third, "call_stdin_yield", "timed out") {
		t.Fatalf("write_stdin result should not be a timeout: %+v", third["messages"])
	}
}

func TestLiveBinary_ExecCommandOmitsBinaryOutputFromTranscript(t *testing.T) {
	bin := buildJuex(t)
	work := t.TempDir()
	if err := writeText(filepath.Join(work, "emit_binary.go"), `package main

import "os"

func main() {
	data := []byte{0x00, 0x01, 'P', 'N', 'G'}
	for i := 0; i < 1024; i++ {
		data = append(data, byte(i%251))
	}
	_, _ = os.Stdout.Write(data)
}
`); err != nil {
		t.Fatal(err)
	}
	helperName := "emit-binary"
	if runtime.GOOS == "windows" {
		helperName += ".exe"
	}
	helperPath := filepath.Join(work, helperName)
	buildHelper := exec.Command("go", "build", "-o", helperPath, "emit_binary.go")
	buildHelper.Dir = work
	if out, err := buildHelper.CombinedOutput(); err != nil {
		t.Fatalf("build binary-output helper: %v\n%s", err, out)
	}
	helperCommand := shQuote(helperPath)
	if runtime.GOOS == "windows" {
		helperCommand = "& '" + strings.ReplaceAll(helperPath, "'", "''") + "'"
	}

	var requestCount atomic.Int32
	var mu sync.Mutex
	var secondBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")

		switch requestCount.Add(1) {
		case 1:
			writeJSON(t, w, chatToolCallResponse("call_exec_binary", "exec_command", map[string]any{
				"cmd":           helperCommand,
				"yield_time_ms": 30000,
			}))
		case 2:
			mu.Lock()
			secondBody = body
			mu.Unlock()
			writeJSON(t, w, chatCompletionResponseMap("binary output handled"))
		default:
			t.Errorf("unexpected provider request %d: %+v", requestCount.Load(), body)
			writeJSON(t, w, chatCompletionResponseMap("unexpected"))
		}
	}))
	defer srv.Close()

	configPath := filepath.Join(work, ".juex", "juex.yaml")
	body := "models: [local-chat:chat-test]\nproviders:\n" + strings.ReplaceAll(`  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    capabilities:
      streaming: false
    models:
      - id: chat-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, body); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	result := sendAndWait(t, bin, home, work, "run the binary output command")
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2", got)
	}
	if !strings.Contains(threadJournalText(t, result.ThreadDir), "binary output handled") {
		t.Fatalf("Thread journal missing final binary-output response")
	}

	mu.Lock()
	second := cloneMap(secondBody)
	mu.Unlock()
	secondJSON, _ := json.Marshal(second)
	assertBinaryOutputSanitized(t, string(secondJSON))
	if !requestHasToolResult(second, "call_exec_binary", "[binary output omitted:") {
		t.Fatalf("second provider request missing sanitized binary tool result: %+v", second["messages"])
	}

	conversationText := threadJournalText(t, result.ThreadDir)
	assertBinaryOutputSanitized(t, conversationText)
	eventsText := conversationText
	assertBinaryOutputSanitized(t, eventsText)
	if strings.Contains(eventsText, `"type":"tool.output_delta"`) {
		t.Fatalf("events persisted transient tool output delta:\n%s", eventsText)
	}
	for _, want := range []string{`"type":"tool.completed"`, `"content":"`, `"binary_omitted":true`, `"binary_sha256":`, `"first_bytes_hex":"0001504e47`} {
		if !strings.Contains(eventsText, want) {
			t.Fatalf("events missing %q:\n%s", want, eventsText)
		}
	}
	if strings.Contains(eventsText, `"output":"[binary output omitted:`) {
		t.Fatalf("events duplicated binary placeholder in structured result:\n%s", eventsText)
	}
}

func TestLiveBinary_ProviderErrorPersistsThreadFailure(t *testing.T) {
	bin := buildJuex(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"provider unavailable"}}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	body := "models: [openai:gpt-test]\nproviders:\n" + strings.ReplaceAll(`  - id: openai
    protocol: openai/responses
    base_url: BASE_URL
    api_key: k
    models:
      - id: gpt-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, body); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	result := sendAndWaitFailure(t, bin, home, work, "hello")
	journal := threadJournalText(t, result.ThreadDir)
	if !strings.Contains(journal, `"type":"turn.errored"`) {
		t.Fatalf("Thread journal missing terminal failure after provider error:\n%s", journal)
	}
	pendingPath := filepath.Join(result.ThreadDir, "pending_inputs.json")
	deadline := time.Now().Add(5 * time.Second)
	for {
		pending, err := os.ReadFile(pendingPath)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(pending), `"state": "dead_lettered"`) && strings.Contains(string(pending), "provider unavailable") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending input did not retain the failed attempt:\n%s", pending)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestLiveBinary_ProviderDeadlineErrorJSONIsTimeout(t *testing.T) {
	bin := buildJuex(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"deadline_exceeded"}}`, http.StatusGatewayTimeout)
	}))
	defer srv.Close()

	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	body := "models: [openai:gpt-test]\nproviders:\n" + strings.ReplaceAll(`  - id: openai
    protocol: openai/responses
    base_url: BASE_URL
    api_key: k
    models:
      - id: gpt-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, body); err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	t.Cleanup(func() { stopLiveAgent(t, bin, home, work) })
	result := sendAndWaitFailure(t, bin, home, work, "hello")
	eventsText := threadJournalText(t, result.ThreadDir)
	for _, want := range []string{`"type":"turn.errored"`, `"error_kind":"timeout"`, `"timed_out":true`, `"raw_cause":`} {
		if !strings.Contains(eventsText, want) {
			t.Fatalf("events missing %q:\n%s", want, eventsText)
		}
	}
}

func chatCompletionResponse(text string) string {
	return `{
  "id": "chatcmpl_1",
  "object": "chat.completion",
  "choices": [
    {
      "index": 0,
      "message": {"role": "assistant", "content": "` + text + `"},
      "finish_reason": "stop"
    }
  ],
  "usage": {"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2}
}`
}

func chatCompletionResponseMap(text string) map[string]any {
	return map[string]any{
		"id":     "chatcmpl_1",
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index":         0,
			"message":       map[string]any{"role": "assistant", "content": text},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
}

func chatToolCallResponse(callID, name string, arguments map[string]any) map[string]any {
	args, _ := json.Marshal(arguments)
	return map[string]any{
		"id":     "chatcmpl_tool_1",
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": "",
				"tool_calls": []any{map[string]any{
					"id":   callID,
					"type": "function",
					"function": map[string]any{
						"name":      name,
						"arguments": string(args),
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
}

type providerToolCall struct {
	ID        string
	Name      string
	Arguments map[string]any
}

func chatToolCallsResponse(calls []providerToolCall) map[string]any {
	toolCalls := make([]any, 0, len(calls))
	for _, call := range calls {
		args, _ := json.Marshal(call.Arguments)
		toolCalls = append(toolCalls, map[string]any{
			"id":   call.ID,
			"type": "function",
			"function": map[string]any{
				"name":      call.Name,
				"arguments": string(args),
			},
		})
	}
	return map[string]any{
		"id":     "chatcmpl_tool_batch",
		"object": "chat.completion",
		"choices": []any{map[string]any{
			"index": 0,
			"message": map[string]any{
				"role":       "assistant",
				"content":    "",
				"tool_calls": toolCalls,
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("write json response: %v", err)
	}
}

func requestHasTool(body map[string]any, name string) bool {
	tools, _ := body["tools"].([]any)
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		function, _ := tool["function"].(map[string]any)
		if function["name"] == name {
			return true
		}
	}
	return false
}

func requestHasUserImage(body map[string]any, wantText string) bool {
	messages, _ := body["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "user" {
			continue
		}
		parts, _ := message["content"].([]any)
		var haveText, haveImage bool
		for _, rawPart := range parts {
			part, _ := rawPart.(map[string]any)
			switch part["type"] {
			case "text":
				haveText = strings.Contains(fmt.Sprint(part["text"]), wantText)
			case "image_url":
				imageURL, _ := part["image_url"].(map[string]any)
				haveImage = strings.HasPrefix(fmt.Sprint(imageURL["url"]), "data:image/png;base64,")
			}
		}
		if haveText && haveImage {
			return true
		}
	}
	return false
}

func requestUserContentContains(body map[string]any, wants ...string) bool {
	messages, _ := body["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "user" {
			continue
		}
		var content strings.Builder
		switch value := message["content"].(type) {
		case string:
			content.WriteString(value)
		case []any:
			for _, rawPart := range value {
				part, _ := rawPart.(map[string]any)
				if part["type"] == "text" {
					fmt.Fprint(&content, part["text"])
				}
			}
		}
		text := content.String()
		matched := true
		for _, want := range wants {
			matched = matched && strings.Contains(text, want)
		}
		if matched {
			return true
		}
	}
	return false
}

func requestHasToolResult(body map[string]any, toolCallID string, want string) bool {
	content, ok := providerToolResultContent(body, toolCallID)
	return ok && strings.Contains(content, want)
}

func providerToolResultContent(body map[string]any, toolCallID string) (string, bool) {
	messages, _ := body["messages"].([]any)
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message["role"] != "tool" || message["tool_call_id"] != toolCallID {
			continue
		}
		return fmt.Sprint(message["content"]), true
	}
	return "", false
}

func sessionIDFromProviderToolResult(body map[string]any, toolCallID string) (int, bool) {
	content, ok := providerToolResultContent(body, toolCallID)
	if !ok {
		return 0, false
	}
	for _, line := range strings.Split(content, "\n") {
		if !strings.HasPrefix(line, "Process running with session ID ") {
			continue
		}
		sessionID, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "Process running with session ID ")))
		if err != nil {
			return 0, false
		}
		return sessionID, true
	}
	return 0, false
}

func assertThreadExecCommandToolRoundTrip(t *testing.T, messages []llm.Message, toolUseID string, wantOutput string) {
	t.Helper()

	var sawToolUse bool
	var sawToolResult bool
	for _, message := range messages {
		for _, block := range message.Blocks {
			switch block.Type {
			case llm.BlockToolUse:
				if block.ToolUseID == toolUseID && block.ToolName == "exec_command" {
					sawToolUse = true
				}
			case llm.BlockToolResult:
				if block.ToolUseID == toolUseID && strings.Contains(block.Content, wantOutput) && strings.Contains(block.Content, "Process exited with code 0") {
					sawToolResult = true
				}
			}
		}
	}
	if !sawToolUse {
		t.Fatalf("Thread journal missing exec_command tool_use with id %q", toolUseID)
	}
	if !sawToolResult {
		t.Fatalf("Thread journal missing tool_result for %q containing command output %q", toolUseID, wantOutput)
	}
}

func assertBinaryOutputSanitized(t *testing.T, text string) {
	t.Helper()
	for _, want := range []string{"[binary output omitted:", "first_bytes_hex=0001504e47"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text missing binary placeholder marker %q:\n%s", want, text)
		}
	}
	for _, forbidden := range []string{`\u0000`, "\x00", "PNG"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("text contains raw binary marker %q:\n%s", forbidden, text)
		}
	}
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func slowShellYieldCommand() string {
	if runtime.GOOS == "windows" {
		return "[Console]::Out.WriteLine('slow start'); [Console]::Out.Flush(); Start-Sleep -Seconds 3; [Console]::Out.WriteLine('slow done'); [Console]::Out.Flush()"
	}
	return "printf 'slow start\\n'; sleep 3; printf 'slow done\\n'"
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func writeText(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
