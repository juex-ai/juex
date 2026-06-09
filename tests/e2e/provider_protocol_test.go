package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
