package fleet

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/runtime"
)

const (
	maxRestartResponseBytes = 64 << 10
	restartActivityPath     = "/api/status"
	restartResumePrompt     = "System notice: this agent was restarted while the previous turn was active. Review the session notes and recent tool results, then continue the interrupted work."
)

func readRestartActivity(ctx context.Context, state endpoint.Runtime) (restartActivity, error) {
	target, err := endpoint.Parse(state.Endpoint)
	if err != nil {
		return restartActivity{}, fmt.Errorf("parse agent endpoint: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		target.URL(restartActivityPath),
		nil,
	)
	if err != nil {
		return restartActivity{}, fmt.Errorf("build restart activity request: %w", err)
	}
	response, err := target.NewClient().Do(request)
	if err != nil {
		return restartActivity{}, fmt.Errorf("read restart activity: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return restartActivity{}, restartHTTPStatusError(response, http.MethodGet, restartActivityPath)
	}

	var body struct {
		SessionID string `json:"session_id"`
		Status    *struct {
			Session runtime.SessionRuntimeStatus `json:"session"`
		} `json:"status"`
	}
	if err := decodeRestartResponse(response.Body, &body); err != nil {
		return restartActivity{}, fmt.Errorf("decode restart activity: %w", err)
	}
	if body.Status == nil {
		return restartActivity{}, nil
	}
	sessionID := strings.TrimSpace(body.Status.Session.ID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(body.SessionID)
	}
	activity := restartActivity{
		SessionID: sessionID,
		State:     body.Status.Session.State,
	}
	if (activity.State == runtime.SessionRuntimeTurnActive ||
		activity.State == runtime.SessionRuntimeDrainingPending) &&
		activity.SessionID == "" {
		return restartActivity{}, fmt.Errorf("active restart activity omitted session id")
	}
	return activity, nil
}

func postRestartResume(
	ctx context.Context,
	state endpoint.Runtime,
	sessionID string,
	prompt string,
) (string, error) {
	target, err := endpoint.Parse(state.Endpoint)
	if err != nil {
		return "", fmt.Errorf("parse agent endpoint: %w", err)
	}
	body, err := json.Marshal(struct {
		Prompt string `json:"prompt"`
	}{Prompt: prompt})
	if err != nil {
		return "", fmt.Errorf("encode restart continuation: %w", err)
	}
	path := "/api/sessions/" + url.PathEscape(sessionID) + "/turns"
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		target.URL(path),
		bytes.NewReader(body),
	)
	if err != nil {
		return "", fmt.Errorf("build restart continuation request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := target.NewClient().Do(request)
	if err != nil {
		return "", fmt.Errorf("submit restart continuation: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", restartHTTPStatusError(response, http.MethodPost, path)
	}
	var result struct {
		TurnID string `json:"turn_id"`
	}
	if err := decodeRestartResponse(response.Body, &result); err != nil {
		return "", fmt.Errorf("decode restart continuation: %w", err)
	}
	result.TurnID = strings.TrimSpace(result.TurnID)
	if result.TurnID == "" {
		return "", fmt.Errorf("restart continuation response omitted turn_id")
	}
	return result.TurnID, nil
}

func decodeRestartResponse(reader io.Reader, target any) error {
	limited := io.LimitReader(reader, maxRestartResponseBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if len(body) > maxRestartResponseBytes {
		return fmt.Errorf("response exceeds %d bytes", maxRestartResponseBytes)
	}
	if err := json.Unmarshal(body, target); err != nil {
		return err
	}
	return nil
}

func restartHTTPStatusError(
	response *http.Response,
	method string,
	path string,
) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	return &endpoint.HTTPStatusError{
		Method:     method,
		Path:       path,
		StatusCode: response.StatusCode,
		Body:       string(bytes.TrimSpace(body)),
	}
}
