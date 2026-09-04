package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/fleet"
	"github.com/juex-ai/juex/internal/thread"
)

type agentClient struct {
	agentID   string
	workspace string
	target    endpoint.Target
	client    *http.Client
	transport *http.Transport
}

func connectSelectedAgent(ctx context.Context, manager *fleet.Manager, state fleet.ReadOnlyAgentState) (*agentClient, error) {
	_, startErr := manager.Start(ctx, state.ID)
	if startErr != nil {
		return nil, mapFleetError(fmt.Errorf("start Agent Runtime: %w", startErr))
	}
	running, err := manager.Endpoint(ctx, state.ID)
	if err != nil {
		return nil, mapFleetError(err)
	}
	target, err := endpoint.Parse(running.Endpoint)
	if err != nil {
		return nil, err
	}
	transport := target.NewTransport()
	return &agentClient{
		agentID: state.ID, workspace: state.Workspace, target: target,
		client: &http.Client{Transport: transport}, transport: transport,
	}, nil
}

func (c *agentClient) Close() {
	if c != nil && c.transport != nil {
		c.transport.CloseIdleConnections()
	}
}

func (c *agentClient) doJSON(ctx context.Context, method, path string, body, result any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.target.URL(path), reader)
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		return mapAgentAPIError(method, path, response.StatusCode, data)
	}
	if result == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(result)
}

type threadList struct {
	Active   []thread.IndexEntry `json:"active_threads"`
	Archived []thread.IndexEntry `json:"archived_threads"`
}

func (c *agentClient) listThreads(ctx context.Context) (threadList, error) {
	var result threadList
	err := c.doJSON(ctx, http.MethodGet, "/api/threads", nil, &result)
	return result, err
}

func (c *agentClient) resolveThread(ctx context.Context, selector string, includeArchived bool) (thread.IndexEntry, error) {
	list, err := c.listThreads(ctx)
	if err != nil {
		return thread.IndexEntry{}, err
	}
	return resolveThreadEntry(selector, list, includeArchived)
}

func resolveThreadEntry(selector string, list threadList, includeArchived bool) (thread.IndexEntry, error) {
	selector = strings.TrimSpace(strings.TrimPrefix(selector, "#"))
	if selector == "" || strings.EqualFold(selector, thread.MainAlias) {
		selector = thread.MainID
	}
	entries := append([]thread.IndexEntry(nil), list.Active...)
	if includeArchived {
		entries = append(entries, list.Archived...)
	}
	for _, entry := range entries {
		if entry.ThreadID == selector {
			return entry, nil
		}
	}
	var match *thread.IndexEntry
	for i := range entries {
		if !strings.EqualFold(entries[i].Alias, selector) {
			continue
		}
		if match != nil {
			return thread.IndexEntry{}, fmt.Errorf("thread alias %q is ambiguous", selector)
		}
		candidate := entries[i]
		match = &candidate
	}
	if match == nil {
		return thread.IndexEntry{}, &notFoundError{msg: "Thread not found: " + selector}
	}
	return *match, nil
}

func (c *agentClient) upload(ctx context.Context, threadID, path string) (map[string]any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filepath.Base(path))
	if err != nil {
		return nil, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	apiPath := "/api/threads/" + url.PathEscape(threadID) + "/attachments"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.target.URL(apiPath), &body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		return nil, mapAgentAPIError(http.MethodPost, apiPath, response.StatusCode, data)
	}
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

type streamEvent struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	TurnID  string          `json:"turn_id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

func (c *agentClient) stream(ctx context.Context, threadID, after string, visit func(streamEvent) (bool, error)) error {
	path := "/api/threads/" + url.PathEscape(threadID) + "/events"
	if after != "" {
		path += "?since=" + url.QueryEscape(after)
	} else {
		path += "?replay=journal-start"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.target.URL(path), nil)
	if err != nil {
		return err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
		return mapAgentAPIError(http.MethodGet, path, response.StatusCode, data)
	}
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			return err
		}
		done, err := visit(event)
		if err != nil || done {
			return err
		}
	}
	return scanner.Err()
}

func mapAgentAPIError(method, path string, status int, body []byte) error {
	message := strings.TrimSpace(string(body))
	var payload struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &payload) == nil {
		if strings.TrimSpace(payload.Message) != "" {
			message = strings.TrimSpace(payload.Message)
		} else if strings.TrimSpace(payload.Error) != "" {
			message = strings.TrimSpace(payload.Error)
		}
	}
	if message == "" {
		message = http.StatusText(status)
	}
	text := fmt.Sprintf("agent API %s %s: %s", method, path, message)
	switch status {
	case http.StatusBadRequest:
		return &usageError{msg: text}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &permissionError{msg: text}
	case http.StatusNotFound:
		return &notFoundError{msg: text}
	case http.StatusConflict:
		return &conflictError{msg: text}
	default:
		return fmt.Errorf("%s (HTTP %d)", text, status)
	}
}
