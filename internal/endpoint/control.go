package endpoint

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	identityPath = "/api/identity"
	shutdownPath = "/api/control/shutdown"
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

func (e *HTTPStatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("endpoint: %s %s returned HTTP %d", e.Method, e.Path, e.StatusCode)
	}
	return fmt.Sprintf("endpoint: %s %s returned HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

func Probe(ctx context.Context, expected Runtime) error {
	target, err := Parse(expected.Endpoint)
	if err != nil {
		return fmt.Errorf("endpoint: parse runtime endpoint: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.URL(identityPath), nil)
	if err != nil {
		return fmt.Errorf("endpoint: build identity request: %w", err)
	}
	response, err := target.NewClient().Do(request)
	if err != nil {
		return fmt.Errorf("endpoint: probe runtime identity: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return responseStatusError(response, http.MethodGet, identityPath)
	}
	var actual Runtime
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&actual); err != nil {
		return fmt.Errorf("endpoint: decode runtime identity: %w", err)
	}
	if !actual.Matches(expected) {
		return &IdentityMismatchError{Expected: expected, Actual: actual}
	}
	return nil
}

func RequestShutdown(ctx context.Context, expected Runtime) error {
	target, err := Parse(expected.Endpoint)
	if err != nil {
		return fmt.Errorf("endpoint: parse runtime endpoint: %w", err)
	}
	body, err := json.Marshal(expected)
	if err != nil {
		return fmt.Errorf("endpoint: encode shutdown identity: %w", err)
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		target.URL(shutdownPath),
		bytes.NewReader(body),
	)
	if err != nil {
		return fmt.Errorf("endpoint: build shutdown request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := target.NewClient().Do(request)
	if err != nil {
		return fmt.Errorf("endpoint: request runtime shutdown: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return responseStatusError(response, http.MethodPost, shutdownPath)
	}
	return nil
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
