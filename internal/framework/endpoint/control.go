package endpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	identityPath     = "/api/identity"
	shutdownPath     = "/api/control/shutdown"
	idleShutdownPath = "/api/control/shutdown-idle"
)

type IdentityMismatchError struct {
	Expected Runtime
	Actual   Runtime
}

func (e *IdentityMismatchError) Error() string {
	return fmt.Sprintf(
		"endpoint: runtime identity mismatch: expected agent %q instance %q, got agent %q instance %q",
		e.Expected.AgentID,
		e.Expected.InstanceID,
		e.Actual.AgentID,
		e.Actual.InstanceID,
	)
}

type HTTPStatusError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

type ShutdownReason string

const ShutdownReasonRuntimeRestart ShutdownReason = "runtime_restart"

type ShutdownRequest struct {
	Runtime
	Reason ShutdownReason `json:"reason,omitempty"`
}

type ShutdownResponse struct {
	Status        string         `json:"status"`
	RestartIntent ShutdownReason `json:"restart_intent,omitempty"`
	IdleReserved  bool           `json:"idle_reserved,omitempty"`
}

var ErrRuntimeBusy = errors.New("endpoint: runtime is busy; retry when idle")

func RequestShutdownIfIdle(ctx context.Context, expected Runtime) error {
	response, err := requestShutdownAt(ctx, ShutdownRequest{Runtime: expected}, idleShutdownPath)
	var status *HTTPStatusError
	if errors.As(err, &status) && status.StatusCode == http.StatusConflict && bytes.Contains([]byte(status.Body), []byte(`"runtime_busy"`)) {
		return ErrRuntimeBusy
	}
	if err != nil {
		return err
	}
	if !response.IdleReserved {
		return errors.New("endpoint: runtime did not acknowledge idle reservation")
	}
	return nil
}

func (e *HTTPStatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("endpoint: %s %s returned HTTP %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("endpoint: %s %s returned HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func Probe(ctx context.Context, expected Runtime) error {
	_, err := Inspect(ctx, expected)
	return err
}

func Inspect(ctx context.Context, expected Runtime) (Runtime, error) {
	target, err := Parse(expected.Endpoint)
	if err != nil {
		return Runtime{}, fmt.Errorf("endpoint: parse runtime endpoint: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL(identityPath), nil)
	if err != nil {
		return Runtime{}, fmt.Errorf("endpoint: build identity request: %w", err)
	}
	response, err := target.NewClient().Do(request)
	if err != nil {
		return Runtime{}, fmt.Errorf("endpoint: probe runtime identity: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Runtime{}, responseStatusError(response, http.MethodGet, identityPath)
	}
	var actual Runtime
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&actual); err != nil {
		return Runtime{}, fmt.Errorf("endpoint: decode runtime identity: %w", err)
	}
	if !actual.Matches(expected) {
		return Runtime{}, &IdentityMismatchError{Expected: expected, Actual: actual}
	}
	return actual, nil
}

func RequestShutdown(ctx context.Context, expected Runtime) error {
	_, err := requestShutdown(ctx, ShutdownRequest{Runtime: expected})
	return err
}

// RequestRestart sends an explicit restart intent through the identity-bound
// shutdown endpoint and reports whether the running agent acknowledged it.
func RequestRestart(ctx context.Context, expected Runtime) (bool, error) {
	response, err := requestShutdown(ctx, ShutdownRequest{
		Runtime: expected,
		Reason:  ShutdownReasonRuntimeRestart,
	})
	if err != nil {
		return false, err
	}
	return response.RestartIntent == ShutdownReasonRuntimeRestart, nil
}

func requestShutdown(ctx context.Context, shutdown ShutdownRequest) (ShutdownResponse, error) {
	return requestShutdownAt(ctx, shutdown, shutdownPath)
}

func requestShutdownAt(ctx context.Context, shutdown ShutdownRequest, path string) (ShutdownResponse, error) {
	expected := shutdown.Runtime
	target, err := Parse(expected.Endpoint)
	if err != nil {
		return ShutdownResponse{}, fmt.Errorf("endpoint: parse runtime endpoint: %w", err)
	}
	body, err := json.Marshal(shutdown)
	if err != nil {
		return ShutdownResponse{}, fmt.Errorf("endpoint: encode shutdown identity: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		target.URL(path),
		bytes.NewReader(body),
	)
	if err != nil {
		return ShutdownResponse{}, fmt.Errorf("endpoint: build shutdown request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := target.NewClient().Do(request)
	if err != nil {
		return ShutdownResponse{}, fmt.Errorf("endpoint: request runtime shutdown: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return ShutdownResponse{}, responseStatusError(response, http.MethodPost, path)
	}
	var result ShutdownResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&result); err != nil &&
		!errors.Is(err, io.EOF) {
		return ShutdownResponse{}, fmt.Errorf("endpoint: decode shutdown response: %w", err)
	}
	return result, nil
}

func responseStatusError(response *http.Response, method, path string) error {
	body, _ := io.ReadAll(io.LimitReader(response.Body, 4<<10))
	return &HTTPStatusError{
		Method:     method,
		Path:       path,
		StatusCode: response.StatusCode,
		Body:       string(bytes.TrimSpace(body)),
	}
}
