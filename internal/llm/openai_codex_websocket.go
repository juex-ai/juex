package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"runtime"
	"strings"
	"sync"

	"github.com/coder/websocket"
	"github.com/openai/openai-go/responses"
)

const codexResponsesWebsocketBeta = "responses_websockets=2026-02-06"

type codexResponsesWebsocketTransport struct {
	profile    ProviderProfile
	httpClient *http.Client

	mu             sync.Mutex
	conn           *websocket.Conn
	lastRequest    map[string]any
	lastBaseline   []any
	lastResponseID string
}

func newCodexResponsesWebsocketTransport(profile ProviderProfile, httpClient *http.Client) *codexResponsesWebsocketTransport {
	return &codexResponsesWebsocketTransport{profile: profile, httpClient: httpClient}
}

func (t *codexResponsesWebsocketTransport) Complete(ctx context.Context, params responses.ResponseNewParams) (*responses.Response, error) {
	payload, err := codexResponsesWebsocketPayload(params)
	if err != nil {
		return nil, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	conn, err := t.ensureConnLocked(ctx)
	if err != nil {
		return nil, err
	}

	frame := t.prepareFrameLocked(payload)
	frameBytes, err := json.Marshal(frame)
	if err != nil {
		return nil, fmt.Errorf("codex websocket encode request: %w", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, frameBytes); err != nil {
		t.closeLocked()
		return nil, fmt.Errorf("codex websocket send request: %w", err)
	}

	resp, err := readCodexResponsesWebsocket(ctx, conn)
	if err != nil {
		t.closeLocked()
		return nil, err
	}
	if t.profile.Compat.CodexTransport == CodexTransportWebSocket {
		t.closeLocked()
		return resp, nil
	}
	t.lastRequest = cloneJSONMap(payload)
	t.lastBaseline = codexResponsesWebsocketBaseline(payload, resp, t.profile)
	t.lastResponseID = resp.ID
	return resp, nil
}

func (t *codexResponsesWebsocketTransport) ensureConnLocked(ctx context.Context) (*websocket.Conn, error) {
	if t.conn != nil {
		return t.conn, nil
	}
	wsURL, err := codexResponsesWebsocketURL(t.profile)
	if err != nil {
		return nil, err
	}
	conn, resp, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPClient:      t.httpClient,
		HTTPHeader:      codexResponsesWebsocketHeaders(t.profile),
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("codex websocket connect: status %d: %w", resp.StatusCode, err)
		}
		return nil, fmt.Errorf("codex websocket connect: %w", err)
	}
	t.conn = conn
	return conn, nil
}

func (t *codexResponsesWebsocketTransport) prepareFrameLocked(payload map[string]any) map[string]any {
	frame := cloneJSONMap(payload)
	if delta, ok := codexResponsesWebsocketDelta(payload, t.lastRequest, t.lastBaseline); ok && t.lastResponseID != "" {
		frame["input"] = delta
		frame["previous_response_id"] = t.lastResponseID
	} else {
		delete(frame, "previous_response_id")
	}
	frame["type"] = "response.create"
	return frame
}

func (t *codexResponsesWebsocketTransport) closeLocked() {
	if t.conn != nil {
		_ = t.conn.Close(websocket.StatusNormalClosure, "")
		t.conn = nil
	}
	t.lastRequest = nil
	t.lastBaseline = nil
	t.lastResponseID = ""
}

func codexResponsesWebsocketPayload(params responses.ResponseNewParams) (map[string]any, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("codex websocket encode params: %w", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("codex websocket decode params: %w", err)
	}
	payload["stream"] = true
	return payload, nil
}

func codexResponsesWebsocketDelta(current, previous map[string]any, baseline []any) ([]any, bool) {
	if current == nil || previous == nil || len(baseline) == 0 {
		return nil, false
	}
	currentWithoutInput := cloneJSONMap(current)
	previousWithoutInput := cloneJSONMap(previous)
	delete(currentWithoutInput, "input")
	delete(previousWithoutInput, "input")
	if !reflect.DeepEqual(currentWithoutInput, previousWithoutInput) {
		return nil, false
	}
	input, ok := current["input"].([]any)
	if !ok || len(input) < len(baseline) {
		return nil, false
	}
	for i := range baseline {
		if !reflect.DeepEqual(input[i], baseline[i]) {
			return nil, false
		}
	}
	return append([]any(nil), input[len(baseline):]...), true
}

func codexResponsesWebsocketBaseline(payload map[string]any, resp *responses.Response, profile ProviderProfile) []any {
	input, _ := payload["input"].([]any)
	baseline := append([]any(nil), input...)
	return append(baseline, codexResponsesOutputAsInput(resp, profile)...)
}

func codexResponsesOutputAsInput(resp *responses.Response, profile ProviderProfile) []any {
	if resp == nil || len(resp.Output) == 0 {
		return nil
	}
	assistant := Message{Role: RoleAssistant}
	for _, item := range resp.Output {
		switch item.Type {
		case "message":
			for _, c := range item.Content {
				switch c.Type {
				case "output_text":
					assistant.Blocks = append(assistant.Blocks, Block{Type: BlockText, Text: c.Text})
				case "refusal":
					assistant.Blocks = append(assistant.Blocks, Block{Type: BlockText, Text: c.Refusal})
				}
			}
		case "function_call":
			assistant.Blocks = append(assistant.Blocks, Block{
				Type:      BlockToolUse,
				ToolUseID: item.CallID,
				ToolName:  item.Name,
				Input:     parseToolArguments(item.Arguments),
			})
		}
	}
	if len(assistant.Blocks) == 0 {
		return nil
	}
	encoded := toOpenAIResponseInputWithOptions([]Message{assistant}, profile, responseInputOptions{OmitReasoning: true})
	raw, err := json.Marshal(encoded)
	if err != nil {
		return nil
	}
	var items []any
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	return items
}

func codexResponsesWebsocketURL(profile ProviderProfile) (string, error) {
	u, err := url.Parse(openAICodexResponsesBaseURL(profile.BaseURL))
	if err != nil {
		return "", fmt.Errorf("codex websocket URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("codex websocket URL: unsupported scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/responses"
	q := u.Query()
	for k, v := range profile.Query {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func codexResponsesWebsocketHeaders(profile ProviderProfile) http.Header {
	headers := http.Header{}
	for k, v := range profile.Headers {
		headers.Set(k, v)
	}
	if profile.APIKey != "" {
		headers.Set("Authorization", "Bearer "+profile.APIKey)
	}
	headers.Set("originator", "juex")
	headers.Set("User-Agent", fmt.Sprintf("juex (%s; %s)", runtime.GOOS, runtime.GOARCH))
	headers.Set("OpenAI-Beta", codexResponsesWebsocketBeta)
	if accountID := codexAccountID(profile); accountID != "" {
		headers.Set("chatgpt-account-id", accountID)
	}
	return headers
}

func readCodexResponsesWebsocket(ctx context.Context, conn *websocket.Conn) (*responses.Response, error) {
	var (
		finalResp responses.Response
		hasFinal  bool
		items     []responses.ResponseOutputItemUnion
	)
	for {
		messageType, data, err := conn.Read(ctx)
		if err != nil {
			return nil, fmt.Errorf("stream closed before response.completed: %w", err)
		}
		if messageType != websocket.MessageText {
			return nil, fmt.Errorf("unexpected binary websocket event")
		}
		if err := codexWrappedWebsocketError(data); err != nil {
			return nil, err
		}
		var event responses.ResponseStreamEventUnion
		if err := json.Unmarshal(data, &event); err != nil {
			continue
		}
		switch event.Type {
		case "error":
			return nil, fmt.Errorf("codex websocket error: %s", firstNonEmpty(event.Message, event.Code, event.RawJSON()))
		case "response.failed":
			if msg := responseErrorMessage(event.Response); msg != "" {
				return nil, fmt.Errorf("%s", msg)
			}
			return nil, fmt.Errorf("codex websocket response failed")
		case "response.output_item.done":
			items = append(items, event.Item)
		case "response.done", "response.completed", "response.incomplete":
			finalResp = event.Response
			hasFinal = true
		}
		if hasFinal {
			break
		}
	}
	if !hasFinal {
		return nil, fmt.Errorf("stream closed before response.completed")
	}
	if len(finalResp.Output) == 0 && len(items) > 0 {
		finalResp.Output = items
	}
	return &finalResp, nil
}

func codexWrappedWebsocketError(data []byte) error {
	var wrapped struct {
		Type       string `json:"type"`
		Status     int    `json:"status"`
		StatusCode int    `json:"status_code"`
		Error      *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(data, &wrapped); err != nil || wrapped.Type != "error" || wrapped.Error == nil {
		return nil
	}
	status := wrapped.Status
	if status == 0 {
		status = wrapped.StatusCode
	}
	if status >= 200 && status < 300 {
		return nil
	}
	message := firstNonEmpty(wrapped.Error.Message, wrapped.Error.Code, wrapped.Error.Type, string(data))
	if status > 0 {
		return fmt.Errorf("codex websocket error: status %d: %s", status, message)
	}
	return fmt.Errorf("codex websocket error: %s", message)
}

func cloneJSONMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
