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

	"github.com/juex-ai/juex/internal/llm"
)

func TestLiveBinary_ProviderProtocolAndThinkingMatrix(t *testing.T) {
	bin := buildJuex(t)

	cases := []struct {
		name                  string
		modelRef              string
		providerYAML          string
		wantPathSuffix        string
		wantReasoningEffort   string
		wantNoReasoningEffort bool
		responseBody          string
	}{
		{
			name:                "openai responses sends reasoning effort",
			modelRef:            "openai/gpt-test",
			wantPathSuffix:      "/responses",
			wantReasoningEffort: "high",
			providerYAML: `  - id: openai
    protocol: openai/responses
    base_url: BASE_URL
    api_key: k
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
			modelRef:            "local-chat/chat-test",
			wantPathSuffix:      "/chat/completions",
			wantReasoningEffort: "xhigh",
			providerYAML: `  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    models:
      - id: chat-test
        thinking_effort: xhigh
`,
			responseBody: chatCompletionResponse("chat-ok"),
		},
		{
			name:                "deepseek preset uses openai chat reasoning effort",
			modelRef:            "deepseek/deepseek-v4-pro",
			wantPathSuffix:      "/chat/completions",
			wantReasoningEffort: "max",
			providerYAML: `  - id: deepseek
    base_url: BASE_URL
    api_key: k
    models:
      - id: deepseek-v4-pro
        thinking_effort: max
`,
			responseBody: chatCompletionResponse("deepseek-ok"),
		},
		{
			name:                  "capability can disable reasoning effort",
			modelRef:              "local-chat/chat-test",
			wantPathSuffix:        "/chat/completions",
			wantNoReasoningEffort: true,
			providerYAML: `  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    capabilities:
      reasoning_effort: false
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
			body := "model: " + tc.modelRef + "\nproviders:\n" +
				strings.ReplaceAll(tc.providerYAML, "BASE_URL", srv.URL)
			if err := writeText(configPath, body); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(bin, "-C", work, "run", "--json", "hello")
			home := t.TempDir()
			cmd.Env = isolatedJuexBinaryEnv(home)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("juex run: %v\n%s", err, out)
			}
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
				if !ok || reasoning["effort"] != tc.wantReasoningEffort {
					t.Fatalf("responses reasoning = %+v, want effort %q; body=%+v", reasoning, tc.wantReasoningEffort, captured.body)
				}
			} else if got := captured.body["reasoning_effort"]; got != tc.wantReasoningEffort {
				t.Fatalf("reasoning_effort = %v, want %q; body=%+v", got, tc.wantReasoningEffort, captured.body)
			}
		})
	}
}

type capturedProviderRequest struct {
	path string
	body map[string]any
}

func TestLiveBinary_CLIRunExecCommandTool(t *testing.T) {
	bin := buildJuex(t)

	const marker = "JUEX_CLI_EXEC_E2E"
	var requestCount atomic.Int32
	var mu sync.Mutex
	var firstBody map[string]any
	var secondBody map[string]any

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
				"cmd": "echo " + marker,
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
	body := "model: local-chat/chat-test\nproviders:\n" + strings.ReplaceAll(`  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    models:
      - id: chat-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, body); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "-C", work, "--debug", "run", "--new", "--json", "run the exec command e2e marker")
	home := t.TempDir()
	cmd.Env = isolatedJuexBinaryEnv(home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("juex run: %v\n%s", err, out)
	}
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2", got)
	}

	var result struct {
		Text       string `json:"text"`
		SessionID  string `json:"session_id"`
		SessionDir string `json:"session_dir"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("stdout is not run JSON: %v\n%s", err, out)
	}
	if result.Text != "cli exec command complete" || result.SessionID == "" || result.SessionDir == "" {
		t.Fatalf("run result = %+v", result)
	}

	mu.Lock()
	first := cloneMap(firstBody)
	second := cloneMap(secondBody)
	mu.Unlock()
	if !requestHasTool(first, "exec_command") || !requestHasTool(first, "write_stdin") || !requestHasTool(first, "list_shell_sessions") {
		t.Fatalf("first provider request missing shell tool family: %+v", first["tools"])
	}
	if requestHasTool(first, "shell") || requestHasTool(first, "shell_input") {
		t.Fatalf("first provider request exposed legacy shell tools: %+v", first["tools"])
	}
	if !requestHasToolResult(second, "call_exec_cli", marker) {
		t.Fatalf("second provider request missing exec_command result containing %q: %+v", marker, second["messages"])
	}

	conversationPath := filepath.Join(result.SessionDir, "conversation.jsonl")
	assertConversationExecCommandToolRoundTrip(t, conversationPath, "call_exec_cli", marker)
	for _, rel := range []string{"logs/juex.log", "logs/debug.log", "trace.jsonl", "spans.jsonl", "tools.jsonl"} {
		if _, err := os.Stat(filepath.Join(result.SessionDir, rel)); err != nil {
			t.Fatalf("debug artifact %s missing: %v", rel, err)
		}
	}
	trace := readJSONLObjects(t, filepath.Join(result.SessionDir, "trace.jsonl"))
	for _, want := range []string{"tool.completed", "finish.attempted"} {
		if !jsonlHasString(trace, "event", want) {
			t.Fatalf("trace missing %q: %+v", want, trace)
		}
	}
	if spans := readJSONLObjects(t, filepath.Join(result.SessionDir, "spans.jsonl")); len(spans) == 0 {
		t.Fatalf("spans.jsonl should be parseable and non-empty")
	}
	if tools := readJSONLObjects(t, filepath.Join(result.SessionDir, "tools.jsonl")); !jsonlHasString(tools, "event", "tool.completed") {
		t.Fatalf("tools missing tool.completed: %+v", tools)
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
	body := "model: local-chat/chat-test\nruntime:\n  tool_timeout: 1s\nproviders:\n" + strings.ReplaceAll(`  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    models:
      - id: chat-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, body); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "-C", work, "--debug", "run", "--new", "--json", "run the shell yield timeout e2e")
	home := t.TempDir()
	cmd.Env = isolatedJuexBinaryEnv(home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("juex run: %v\n%s", err, out)
	}
	if got := requestCount.Load(); got != 3 {
		t.Fatalf("provider requests = %d, want 3", got)
	}

	var result struct {
		Text       string `json:"text"`
		SessionID  string `json:"session_id"`
		SessionDir string `json:"session_dir"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("stdout is not run JSON: %v\n%s", err, out)
	}
	if result.Text != "yield semantics complete" || result.SessionDir == "" {
		t.Fatalf("run result = %+v", result)
	}

	mu.Lock()
	second := cloneMap(secondBody)
	third := cloneMap(thirdBody)
	mu.Unlock()
	if !requestHasToolResult(second, "call_exec_yield", "Process running with session ID") ||
		!requestHasToolResult(second, "call_exec_yield", "slow start") {
		t.Fatalf("second provider request missing running exec result: %+v", second["messages"])
	}
	if requestHasToolResult(second, "call_exec_yield", "timed out") {
		t.Fatalf("exec_command result should not be a timeout: %+v", second["messages"])
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
				"cmd":           "go run emit_binary.go",
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
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	body := "model: local-chat/chat-test\nproviders:\n" + strings.ReplaceAll(`  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    models:
      - id: chat-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, body); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "-C", work, "--debug", "run", "--new", "--json", "run the binary output command")
	home := t.TempDir()
	cmd.Env = isolatedJuexBinaryEnv(home)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("juex run: %v\n%s", err, out)
	}
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("provider requests = %d, want 2", got)
	}

	var result struct {
		Text       string `json:"text"`
		SessionID  string `json:"session_id"`
		SessionDir string `json:"session_dir"`
	}
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatalf("stdout is not run JSON: %v\n%s", err, out)
	}
	if result.Text != "binary output handled" || result.SessionDir == "" {
		t.Fatalf("run result = %+v", result)
	}

	mu.Lock()
	second := cloneMap(secondBody)
	mu.Unlock()
	secondJSON, _ := json.Marshal(second)
	assertBinaryOutputSanitized(t, string(secondJSON))
	if !requestHasToolResult(second, "call_exec_binary", "[binary output omitted:") {
		t.Fatalf("second provider request missing sanitized binary tool result: %+v", second["messages"])
	}

	conversationText := strings.Join(readLines(t, filepath.Join(result.SessionDir, "conversation.jsonl")), "\n")
	assertBinaryOutputSanitized(t, conversationText)
	eventsText := strings.Join(readLines(t, filepath.Join(result.SessionDir, "events.jsonl")), "\n")
	assertBinaryOutputSanitized(t, eventsText)
	for _, want := range []string{`"type":"tool.output_delta"`, `"binary_omitted":true`, `"binary_sha256":`, `"first_bytes_hex":"0001504e47`} {
		if !strings.Contains(eventsText, want) {
			t.Fatalf("events missing %q:\n%s", want, eventsText)
		}
	}
}

func TestLiveBinary_CtrlCCancelsExecCommandTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt process signalling is platform-specific in this e2e")
	}
	bin := buildJuex(t)

	work := t.TempDir()
	startedPath := filepath.Join(work, "exec-started.txt")
	cmdText := "printf started > " + shQuote(startedPath) + "; sleep 30"
	var requestCount atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch requestCount.Add(1) {
		case 1:
			writeJSON(t, w, chatToolCallResponse("call_exec_cancel", "exec_command", map[string]any{
				"cmd": cmdText,
			}))
		default:
			t.Errorf("unexpected provider request %d: %+v", requestCount.Load(), body)
			writeJSON(t, w, chatCompletionResponseMap("unexpected"))
		}
	}))
	defer srv.Close()

	configPath := filepath.Join(work, ".juex", "juex.yaml")
	body := "model: local-chat/chat-test\nproviders:\n" + strings.ReplaceAll(`  - id: local-chat
    protocol: openai/chat
    base_url: BASE_URL
    api_key: k
    models:
      - id: chat-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, body); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "-C", work, "--debug", "run", "--new", "--json", "start cancellable exec")
	home := t.TempDir()
	cmd.Env = isolatedJuexBinaryEnv(home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, startedPath, 5*time.Second)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("send interrupt: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(10 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("juex run did not exit after interrupt")
	}
	if err == nil {
		t.Fatal("expected interrupted run to exit non-zero")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty on JSON error", stdout.String())
	}

	var got struct {
		Error      string         `json:"error"`
		Message    string         `json:"message"`
		Suggestion string         `json:"suggestion"`
		Retryable  bool           `json:"retryable"`
		SessionDir string         `json:"session_dir"`
		Details    map[string]any `json:"details"`
	}
	if jsonErr := json.Unmarshal(stderr.Bytes(), &got); jsonErr != nil {
		t.Fatalf("stderr is not JSON: %v\n%s", jsonErr, stderr.String())
	}
	if got.Error != "interrupted" || got.Message != "run interrupted by signal SIGINT (2)" || got.Retryable {
		t.Fatalf("interrupt JSON = %+v; stderr=%s", got, stderr.String())
	}
	if strings.Contains(got.Message, "by user") {
		t.Fatalf("message should not blame user: %q", got.Message)
	}
	if got.Suggestion == "" || !strings.Contains(got.Suggestion, "stopped externally") {
		t.Fatalf("suggestion = %q, want external stop guidance", got.Suggestion)
	}
	if got.Details["signal"] != "SIGINT" || got.Details["signal_number"] != float64(2) || got.Details["interrupted"] != true {
		t.Fatalf("details = %+v, want SIGINT metadata", got.Details)
	}
	if got.SessionDir == "" {
		t.Fatalf("stderr missing session_dir: %s", stderr.String())
	}
	if requestCount.Load() != 1 {
		t.Fatalf("provider requests = %d, want only initial tool-use request", requestCount.Load())
	}

	assertConversationToolError(t, filepath.Join(got.SessionDir, "conversation.jsonl"), "call_exec_cancel", "run interrupted by signal SIGINT (2)")
	eventsText := strings.Join(readLines(t, filepath.Join(got.SessionDir, "events.jsonl")), "\n")
	for _, want := range []string{`"type":"tool.errored"`, `"type":"turn.errored"`, `"error_kind":"interrupted"`, `"signal":"SIGINT"`, `"signal_number":2`} {
		if !strings.Contains(eventsText, want) {
			t.Fatalf("events missing %q:\n%s", want, eventsText)
		}
	}
}

func TestLiveBinary_ProviderErrorJSONIncludesSessionMetadata(t *testing.T) {
	bin := buildJuex(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"provider unavailable"}}`, http.StatusInternalServerError)
	}))
	defer srv.Close()

	work := t.TempDir()
	configPath := filepath.Join(work, ".juex", "juex.yaml")
	body := "model: openai/gpt-test\nproviders:\n" + strings.ReplaceAll(`  - id: openai
    protocol: openai/responses
    base_url: BASE_URL
    api_key: k
    models:
      - id: gpt-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, body); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "-C", work, "run", "--json", "hello")
	home := t.TempDir()
	cmd.Env = isolatedJuexBinaryEnv(home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected provider failure")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	var got struct {
		Error      string         `json:"error"`
		SessionID  string         `json:"session_id"`
		SessionDir string         `json:"session_dir"`
		WorkDir    string         `json:"work_dir"`
		Details    map[string]any `json:"details"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &got); err != nil {
		t.Fatalf("stderr is not JSON: %v\n%s", err, stderr.String())
	}
	if got.Error != "general_error" {
		t.Fatalf("error = %q, want general_error; stderr=%s", got.Error, stderr.String())
	}
	if got.SessionID == "" || got.SessionDir == "" || got.WorkDir != work {
		t.Fatalf("metadata = %+v, want session id/dir and work dir %s", got, work)
	}
	if got.Details != nil {
		t.Fatalf("details = %+v", got.Details)
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
	body := "model: openai/gpt-test\nproviders:\n" + strings.ReplaceAll(`  - id: openai
    protocol: openai/responses
    base_url: BASE_URL
    api_key: k
    models:
      - id: gpt-test
`, "BASE_URL", srv.URL)
	if err := writeText(configPath, body); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "-C", work, "run", "--json", "hello")
	home := t.TempDir()
	cmd.Env = isolatedJuexBinaryEnv(home)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err == nil {
		t.Fatal("expected provider timeout failure")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	var got struct {
		Error      string `json:"error"`
		Message    string `json:"message"`
		Retryable  bool   `json:"retryable"`
		SessionDir string `json:"session_dir"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &got); err != nil {
		t.Fatalf("stderr is not JSON: %v\n%s", err, stderr.String())
	}
	if got.Error != "timeout" || !got.Retryable {
		t.Fatalf("error JSON = %+v, want retryable timeout; stderr=%s", got, stderr.String())
	}
	if !strings.Contains(got.Message, "timed out") {
		t.Fatalf("message = %q, want timed out", got.Message)
	}
	if strings.Contains(got.Message, "deadline_exceeded") || strings.Contains(got.Message, "context deadline exceeded") {
		t.Fatalf("message = %q, should not expose raw deadline", got.Message)
	}
	if got.SessionDir == "" {
		t.Fatalf("stderr missing session_dir: %s", stderr.String())
	}

	eventsText := strings.Join(readLines(t, filepath.Join(got.SessionDir, "events.jsonl")), "\n")
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

func assertConversationExecCommandToolRoundTrip(t *testing.T, path string, toolUseID string, wantOutput string) {
	t.Helper()

	lines := readLines(t, path)
	var sawToolUse bool
	var sawToolResult bool
	for i, line := range lines {
		var message llm.Message
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("conversation line %d is not a message: %v\n%s", i, err, line)
		}
		for _, block := range message.Blocks {
			switch block.Type {
			case llm.BlockToolUse:
				if block.ToolUseID == toolUseID && block.ToolName == "exec_command" {
					sawToolUse = true
				}
				if block.ToolName == "shell" || block.ToolName == "shell_input" {
					t.Fatalf("conversation contains legacy shell tool_use on line %d: %s", i, line)
				}
			case llm.BlockToolResult:
				if block.ToolUseID == toolUseID && strings.Contains(block.Content, wantOutput) && strings.Contains(block.Content, "Process exited with code 0") {
					sawToolResult = true
				}
			}
		}
	}
	if !sawToolUse {
		t.Fatalf("conversation missing exec_command tool_use with id %q in %s", toolUseID, path)
	}
	if !sawToolResult {
		t.Fatalf("conversation missing tool_result for %q containing command output %q in %s", toolUseID, wantOutput, path)
	}
}

func assertConversationToolError(t *testing.T, path string, toolUseID string, wantError string) {
	t.Helper()

	lines := readLines(t, path)
	var sawToolUse bool
	var sawToolResult bool
	for i, line := range lines {
		var message llm.Message
		if err := json.Unmarshal([]byte(line), &message); err != nil {
			t.Fatalf("conversation line %d is not a message: %v\n%s", i, err, line)
		}
		for _, block := range message.Blocks {
			switch block.Type {
			case llm.BlockToolUse:
				if block.ToolUseID == toolUseID {
					sawToolUse = true
				}
			case llm.BlockToolResult:
				if block.ToolUseID == toolUseID && block.IsError && strings.Contains(block.Content, wantError) {
					sawToolResult = true
				}
			}
		}
	}
	if !sawToolUse {
		t.Fatalf("conversation missing tool_use with id %q in %s", toolUseID, path)
	}
	if !sawToolResult {
		t.Fatalf("conversation missing error tool_result for %q containing %q in %s", toolUseID, wantError, path)
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

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
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

func isolatedJuexBinaryEnv(home string) []string {
	env := append([]string{}, os.Environ()...)
	env = append(env,
		"HOME="+home,
		"USERPROFILE="+home,
		"CODEX_HOME="+filepath.Join(home, "missing-codex-home"),
		"PROVIDER_API_ID=",
		"PROVIDER_API_PROTOCOL=",
		"PROVIDER_API_BASE=",
		"PROVIDER_API_KEY=",
		"PROVIDER_API_MODEL=",
		"PROVIDER_THINKING_EFFORT=",
		"PROVIDER_CONTEXT_WINDOW=",
	)
	return env
}

func writeText(path, body string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), 0o644)
}
