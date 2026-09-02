package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/config"
	"github.com/juex-ai/juex/internal/llm"
	"github.com/juex-ai/juex/internal/mcp"
	"github.com/juex-ai/juex/internal/observable"
	juexruntime "github.com/juex-ai/juex/internal/runtime"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/web"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

type webProvider struct {
	steps     []llm.Response
	calls     int
	histories [][]llm.Message
	mu        sync.Mutex
}

func (p *webProvider) Name() string { return "web-test" }
func (p *webProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls >= len(p.steps) {
		return llm.Response{}, context.DeadlineExceeded
	}
	p.histories = append(p.histories, append([]llm.Message(nil), h...))
	r := p.steps[p.calls]
	p.calls++
	return r, nil
}

func (p *webProvider) history(idx int) []llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if idx < 0 || idx >= len(p.histories) {
		return nil
	}
	return append([]llm.Message(nil), p.histories[idx]...)
}

type interruptibleCompactWebProvider struct {
	mu             sync.Mutex
	calls          int
	compactStarted chan struct{}
	release        chan struct{}
	startOnce      sync.Once
}

func (p *interruptibleCompactWebProvider) Name() string { return "web-compact-cancel" }

func (p *interruptibleCompactWebProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	call := p.calls
	p.calls++
	p.mu.Unlock()
	if call == 0 {
		return llm.Response{
			Message:    llm.TextMessage(llm.RoleAssistant, "seeded"),
			StopReason: llm.StopEndTurn,
		}, nil
	}
	p.startOnce.Do(func() { close(p.compactStarted) })
	select {
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	case <-p.release:
		return llm.Response{}, context.Canceled
	}
}

func TestWeb_TranscriptPageReadsLatestItemsFromEOF(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work}
	threadState, err := thread.New(cfg.ThreadsDir())
	if err != nil {
		t.Fatal(err)
	}
	threadID := threadState.ID
	messages := []llm.Message{
		{ID: "m1", Role: llm.RoleUser, Kind: llm.MessageKindCompact, Blocks: []llm.Block{{Type: llm.BlockText, Text: "summary"}}},
		{ID: "m2", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "call-1", ToolName: "read", Input: map[string]any{"path": "a.txt"}}}},
		{ID: "m3", Role: llm.RoleSystem, Kind: llm.MessageKindPolicyEvent, Blocks: []llm.Block{{Type: llm.BlockText, Text: "hook read completed PreToolUse"}}},
		{ID: "m4", Role: llm.RoleUser, Kind: llm.MessageKindToolResult, Blocks: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call-1", ToolName: "read", Content: "done"}}},
		{ID: "m5", Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockText, Text: "latest"}}},
	}
	if err := threadState.AppendBatch(messages); err != nil {
		_ = threadState.Close()
		t.Fatal(err)
	}
	if err := threadState.Close(); err != nil {
		t.Fatal(err)
	}

	srv := web.NewServer(web.Options{Cfg: cfg, Provider: &webProvider{}})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/threads/" + threadID + "?limit=3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Thread status = %d body=%s", resp.StatusCode, body)
	}
	var got struct {
		Items []struct {
			Message *llm.Message `json:"message"`
		} `json:"items"`
		HasMoreBefore bool `json:"has_more_before"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	// One append batch is one atomic Journal commit. Pagination may exceed the
	// requested item limit rather than split that commit.
	if len(got.Items) != 5 || got.Items[0].Message == nil || got.Items[0].Message.ID != "m1" ||
		got.Items[2].Message == nil || got.Items[2].Message.Kind != llm.MessageKindPolicyEvent ||
		got.Items[3].Message == nil || got.Items[3].Message.Blocks[0].ToolUseID != "call-1" ||
		got.Items[4].Message == nil || got.Items[4].Message.ID != "m5" ||
		!got.HasMoreBefore {
		t.Fatalf("coherent transcript page = %+v", got)
	}
}

func TestWeb_ThreadMetadataLifecycleSurvivesServerRestart(t *testing.T) {
	work := t.TempDir()
	cfg := config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work}
	server := web.NewServer(web.Options{Cfg: cfg, Provider: &webProvider{}})
	httpServer := httptest.NewServer(server.Handler())

	var created thread.Info
	e2eThreadJSON(t, http.MethodPost, httpServer.URL+"/api/threads", `{"alias":" before "}`, http.StatusCreated, &created)
	if created.Alias != "before" {
		t.Fatalf("canonical created alias = %q", created.Alias)
	}
	e2eThreadJSON(t, http.MethodPost, httpServer.URL+"/api/threads", `{"alias":"#reserved"}`, http.StatusBadRequest, nil)
	store := thread.NewStore(cfg.RuntimePaths().StateDir)
	target, err := store.OpenActive(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.BeginNewGeneration(); err != nil {
		t.Fatal(err)
	}
	if err := target.Append(llm.TextMessage(llm.RoleUser, "survives restart")); err != nil {
		t.Fatal(err)
	}
	scratchFile := filepath.Join(target.ScratchpadDir(), "state.txt")
	if err := os.WriteFile(scratchFile, []byte("preserved"), 0o600); err != nil {
		t.Fatal(err)
	}
	before := target.Projection()
	journalPath := filepath.Join(target.Dir, "journal.jsonl")
	journalBefore, err := os.Stat(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}

	var renamed thread.Info
	e2eThreadJSON(t, http.MethodPatch, httpServer.URL+"/api/threads/"+created.ID, `{"alias":" after "}`, http.StatusOK, &renamed)
	if renamed.Alias != "after" {
		t.Fatalf("renamed Thread = %+v", renamed)
	}
	e2eThreadJSON(t, http.MethodPatch, httpServer.URL+"/api/threads/"+created.ID, `{"alias":"#reserved"}`, http.StatusConflict, nil)
	e2eThreadJSON(t, http.MethodPost, httpServer.URL+"/api/threads/"+created.ID+"/archive", "", http.StatusOK, nil)
	httpServer.Close()
	server.Close()

	restarted := web.NewServer(web.Options{Cfg: cfg, Provider: &webProvider{}})
	t.Cleanup(restarted.Close)
	restartedHTTP := httptest.NewServer(restarted.Handler())
	defer restartedHTTP.Close()
	var listed struct {
		Active   []thread.IndexEntry `json:"active_threads"`
		Archived []thread.IndexEntry `json:"archived_threads"`
	}
	e2eThreadJSON(t, http.MethodGet, restartedHTTP.URL+"/api/threads", "", http.StatusOK, &listed)
	if len(listed.Archived) != 1 || listed.Archived[0].ThreadID != created.ID || listed.Archived[0].Alias != "after" {
		t.Fatalf("restarted archived list = %+v", listed.Archived)
	}

	var restored thread.Info
	e2eThreadJSON(t, http.MethodPost, restartedHTTP.URL+"/api/threads/"+created.ID+"/unarchive", "", http.StatusOK, &restored)
	if restored.RetentionState != thread.RetentionActive || restored.ExecutionState != thread.ExecutionIdle ||
		restored.GenerationID != before.CurrentGeneration.ID || restored.Alias != "after" {
		t.Fatalf("restored Thread = %+v, before = %+v", restored, before)
	}
	reopened, err := thread.NewStore(cfg.RuntimePaths().StateDir).OpenActive(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if len(reopened.History) != 1 || reopened.History[0].FirstText() != "survives restart" {
		t.Fatalf("restored history = %+v", reopened.History)
	}
	if data, err := os.ReadFile(filepath.Join(reopened.ScratchpadDir(), "state.txt")); err != nil || string(data) != "preserved" {
		t.Fatalf("restored Scratchpad = %q, %v", data, err)
	}
	journalAfter, err := os.Stat(filepath.Join(reopened.Dir, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if journalAfter.Size() != journalBefore.Size() {
		t.Fatalf("metadata lifecycle changed Journal size from %d to %d", journalBefore.Size(), journalAfter.Size())
	}
}

func e2eThreadJSON(t *testing.T, method, target, body string, wantStatus int, result any) {
	t.Helper()
	request, err := http.NewRequest(method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("%s %s status = %d, want %d, body = %s", method, target, response.StatusCode, wantStatus, payload)
	}
	if result != nil && len(payload) != 0 {
		if err := json.Unmarshal(payload, result); err != nil {
			t.Fatalf("decode %s %s response: %v; body = %s", method, target, err, payload)
		}
	}
}

func TestWeb_RuntimeToolCatalogIncludesMCPDescriptorsWithoutOpeningThread(t *testing.T) {
	work := t.TempDir()
	projectAgents := filepath.Join(work, ".agents")
	if err := os.MkdirAll(projectAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	mcpConfig := mcp.Config{MCPServers: map[string]mcp.ServerSpec{
		"local": {Command: os.Args[0], Env: map[string]string{"JUEX_E2E_MCP": "1"}},
	}}
	body, err := json.Marshal(mcpConfig)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectAgents, "mcp.json"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: &webProvider{},
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/runtime")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		t.Fatalf("runtime status = %d body=%s", resp.StatusCode, payload)
	}
	var got struct {
		Tools struct {
			Count  int `json:"count"`
			Groups []struct {
				Group string `json:"group"`
				Tools []struct {
					Name string `json:"name"`
				} `json:"tools"`
			} `json:"groups"`
		} `json:"tools"`
		MCP struct {
			Servers []struct {
				Name      string `json:"name"`
				ToolCount int    `json:"tool_count"`
				Tools     []struct {
					Name        string         `json:"name"`
					Description string         `json:"description"`
					Schema      map[string]any `json:"schema"`
					Timeout     struct {
						Mode    string `json:"mode"`
						Seconds int    `json:"seconds"`
					} `json:"timeout"`
				} `json:"tools"`
			} `json:"servers"`
		} `json:"mcp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.Tools.Count != 34 || len(got.Tools.Groups) != 8 {
		t.Fatalf("builtin catalog = %+v", got.Tools)
	}
	var observableToolNames []string
	for _, group := range got.Tools.Groups {
		if group.Group != "observable" {
			continue
		}
		for _, tool := range group.Tools {
			observableToolNames = append(observableToolNames, tool.Name)
		}
	}
	if len(observableToolNames) != 7 || !slices.Contains(observableToolNames, "schedule_create") {
		t.Fatalf("observable tools = %v, want seven including schedule_create", observableToolNames)
	}
	if len(got.MCP.Servers) != 1 || got.MCP.Servers[0].Name != "local" || got.MCP.Servers[0].ToolCount != 1 || len(got.MCP.Servers[0].Tools) != 1 {
		t.Fatalf("mcp catalog = %+v", got.MCP)
	}
	echo := got.MCP.Servers[0].Tools[0]
	properties, _ := echo.Schema["properties"].(map[string]any)
	textSchema, _ := properties["text"].(map[string]any)
	if echo.Name != "echo" || echo.Description != "Echo input" || textSchema["type"] != "string" || echo.Timeout.Mode != "bounded" || echo.Timeout.Seconds != 60 {
		t.Fatalf("echo descriptor = %+v", echo)
	}
}

func TestWeb_RemoteMCPToolRoundTrip(t *testing.T) {
	remote := newWebRemoteMCPServer(t)
	work := t.TempDir()
	projectAgents := filepath.Join(work, ".agents")
	if err := os.MkdirAll(projectAgents, 0o755); err != nil {
		t.Fatal(err)
	}
	mcpJSON := fmt.Sprintf(`{"mcpServers":{"remote":{"type":"http","url":%q}}}`, remote.URL)
	if err := os.WriteFile(filepath.Join(projectAgents, "mcp.json"), []byte(mcpJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	provider := &webProvider{steps: []llm.Response{
		{
			Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{
				Type:      llm.BlockToolUse,
				ToolUseID: "remote-call",
				ToolName:  "mcp__remote__echo",
				Input:     map[string]any{"text": "ping"},
			}}},
			StopReason: llm.StopToolUse,
		},
		{
			Message:    llm.TextMessage(llm.RoleAssistant, "remote tool complete"),
			StopReason: llm.StopEndTurn,
		},
	}}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: provider,
	})
	t.Cleanup(srv.Close)
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()

	threadID := createWebMainThread(t, httpServer.URL)
	response, err := http.Post(
		httpServer.URL+"/api/threads/"+threadID+"/inputs",
		"application/json",
		strings.NewReader(`{"prompt":"use the remote tool"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("turn status = %d body=%s", response.StatusCode, body)
	}
	var turn webStartTurnResponse
	if err := json.NewDecoder(response.Body).Decode(&turn); err != nil {
		t.Fatal(err)
	}
	waitForWebTranscript(t, httpServer.URL, threadID, turn.TurnID, 30*time.Second, "remote MCP result", func(messages []webTranscriptMessage) bool {
		for _, message := range messages {
			for _, block := range message.Blocks {
				if block.Type == "text" && block.Text == "remote tool complete" {
					return true
				}
			}
		}
		return false
	})

	history := provider.history(1)
	for _, message := range history {
		for _, block := range message.Blocks {
			if block.Type == llm.BlockToolResult &&
				block.ToolUseID == "remote-call" &&
				strings.Contains(block.Content, "remote: ping") {
				return
			}
		}
	}
	t.Fatalf("provider history does not contain remote tool result: %#v", history)
}

func newWebRemoteMCPServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := sdkmcp.NewServer(
		&sdkmcp.Implementation{Name: "web-remote-test", Version: "1.0.0"},
		nil,
	)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "echo",
		Description: "Echo remote input",
	}, func(
		_ context.Context,
		_ *sdkmcp.CallToolRequest,
		input remoteWebEchoInput,
	) (*sdkmcp.CallToolResult, any, error) {
		return &sdkmcp.CallToolResult{
			Content: []sdkmcp.Content{
				&sdkmcp.TextContent{Text: "remote: " + input.Text},
			},
		}, nil, nil
	})
	handler := sdkmcp.NewStreamableHTTPHandler(
		func(*http.Request) *sdkmcp.Server { return server },
		&sdkmcp.StreamableHTTPOptions{Stateless: true},
	)
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return httpServer
}

type remoteWebEchoInput struct {
	Text string `json:"text"`
}

func TestWeb_TurnRoundTripPersists(t *testing.T) {
	work := t.TempDir()
	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "noted"), StopReason: llm.StopEndTurn},
	}}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// 1. Create thread.
	created, err := http.Post(ts.URL+"/api/threads", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		ID string `json:"thread_id"`
	}
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()
	if c.ID == "" {
		t.Fatal("no Thread id")
	}

	// 2. Submit a turn.
	resp, err := http.Post(ts.URL+"/api/threads/"+c.ID+"/inputs", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 202 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("turn POST status = %d body=%s", resp.StatusCode, body)
	}
	var turn webStartTurnResponse
	if err := json.NewDecoder(resp.Body).Decode(&turn); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if turn.TurnID == "" {
		t.Fatal("turn response missing turn id")
	}

	// 3. Wait until turn shows in transcript.
	waitForWebTranscript(t, ts.URL, c.ID, turn.TurnID, 30*time.Second, "assistant reply", func(messages []webTranscriptMessage) bool {
		for _, m := range messages {
			if m.Role == "assistant" {
				for _, b := range m.Blocks {
					if b.Type == "text" && b.Text == "noted" {
						return true
					}
				}
			}
		}
		return false
	})
}

func TestWeb_InterruptCancelsCompactionWithoutPersistingMarker(t *testing.T) {
	work := t.TempDir()
	prov := &interruptibleCompactWebProvider{
		compactStarted: make(chan struct{}),
		release:        make(chan struct{}),
	}
	compaction := config.DefaultCompactionConfig()
	compaction.KeepRecentTokens = 0
	srv := web.NewServer(web.Options{
		Cfg: config.Config{
			ProviderID: "openai",
			APIKey:     "x",
			Model:      "m",
			WorkDir:    work,
			Compaction: compaction,
		},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	defer close(prov.release)

	threadID := createWebMainThread(t, ts.URL)
	seed, err := http.Post(
		ts.URL+"/api/threads/"+threadID+"/inputs",
		"application/json",
		strings.NewReader(`{"prompt":"`+strings.Repeat("old context ", 200)+`"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	var seededTurn webStartTurnResponse
	if err := json.NewDecoder(seed.Body).Decode(&seededTurn); err != nil {
		seed.Body.Close()
		t.Fatal(err)
	}
	seed.Body.Close()
	waitForWebTranscript(t, ts.URL, threadID, seededTurn.TurnID, 10*time.Second, "seed turn", func(messages []webTranscriptMessage) bool {
		for _, message := range messages {
			for _, block := range message.Blocks {
				if block.Type == "text" && block.Text == "seeded" {
					return true
				}
			}
		}
		return false
	})

	type compactResult struct {
		response *http.Response
		err      error
	}
	compactDone := make(chan compactResult, 1)
	go func() {
		response, requestErr := http.Post(
			ts.URL+"/api/threads/"+threadID+"/inputs",
			"application/json",
			strings.NewReader(`{"prompt":"/compact"}`),
		)
		compactDone <- compactResult{response: response, err: requestErr}
	}()
	select {
	case <-prov.compactStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("compaction provider did not start")
	}

	waitForCondition(t, 5*time.Second, func() bool {
		response, requestErr := http.Get(ts.URL + "/api/threads/" + threadID + "/status")
		if requestErr != nil {
			return false
		}
		defer response.Body.Close()
		var status juexruntime.StatusSnapshot
		return response.StatusCode == http.StatusOK &&
			json.NewDecoder(response.Body).Decode(&status) == nil &&
			status.Turn != nil &&
			status.Turn.Phase == juexruntime.TurnPhaseCompacting &&
			status.Turn.CanInterrupt
	})

	interrupt, err := http.Post(ts.URL+"/api/threads/"+threadID+"/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var interruptBody struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := json.NewDecoder(interrupt.Body).Decode(&interruptBody); err != nil {
		interrupt.Body.Close()
		t.Fatal(err)
	}
	interrupt.Body.Close()
	if !interruptBody.Cancelled {
		t.Fatal("interrupt returned cancelled=false")
	}

	select {
	case result := <-compactDone:
		if result.err != nil {
			t.Fatal(result.err)
		}
		defer result.response.Body.Close()
		var body struct {
			Message string `json:"message"`
		}
		if err := json.NewDecoder(result.response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if result.response.StatusCode != http.StatusInternalServerError ||
			body.Message != "Compaction canceled" {
			t.Fatalf("compact status=%d body=%+v", result.response.StatusCode, body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("compact request did not stop after interrupt")
	}

	waitForCondition(t, 5*time.Second, func() bool {
		response, requestErr := http.Get(ts.URL + "/api/threads/" + threadID + "/status")
		if requestErr != nil {
			return false
		}
		defer response.Body.Close()
		var status juexruntime.StatusSnapshot
		return response.StatusCode == http.StatusOK &&
			json.NewDecoder(response.Body).Decode(&status) == nil &&
			status.Turn != nil &&
			status.Turn.State == juexruntime.TurnLifecycleCancelled &&
			status.LastError != nil &&
			status.LastError.Message == "Compaction canceled"
	})
	messages, err := fetchWebTranscript(http.DefaultClient, ts.URL, threadID)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.Kind == llm.MessageKindCompact {
			t.Fatalf("unexpected compact marker after cancellation: %+v", message)
		}
	}
}

func TestWeb_ComposerImageUpload(t *testing.T) {
	work := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "agent")
	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "image noted"), StopReason: llm.StopEndTurn},
	}}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, AgentStateDir: stateDir},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	threadID := createWebMainThread(t, ts.URL)
	media := uploadWebThreadImage(t, ts.URL, threadID)
	body, err := json.Marshal(struct {
		Prompt      string         `json:"prompt"`
		Attachments []llm.MediaRef `json:"attachments"`
	}{Prompt: "describe this", Attachments: []llm.MediaRef{media}})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/api/threads/"+threadID+"/inputs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("turn POST status = %d body=%s", resp.StatusCode, body)
	}
	var turn webStartTurnResponse
	if err := json.NewDecoder(resp.Body).Decode(&turn); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if turn.TurnID == "" {
		t.Fatal("turn response missing turn id")
	}

	waitForWebTranscript(t, ts.URL, threadID, turn.TurnID, 30*time.Second, "image upload turn", func(messages []webTranscriptMessage) bool {
		hasAssistant := false
		for _, msg := range messages {
			if msg.Role != "assistant" {
				continue
			}
			for _, block := range msg.Blocks {
				if block.Type == "text" && block.Text == "image noted" {
					hasAssistant = true
				}
			}
		}
		if !hasAssistant {
			return false
		}
		for _, msg := range messages {
			if msg.Role != "user" {
				continue
			}
			hasText := false
			hasImage := false
			for _, block := range msg.Blocks {
				if block.Type == "text" && block.Text == "describe this" {
					hasText = true
				}
				if block.Type == "image" && block.Media != nil && block.Media.ArtifactPath == media.ArtifactPath {
					hasImage = true
				}
			}
			if hasText && hasImage {
				return true
			}
		}
		return false
	})

	history := prov.history(0)
	if len(history) == 0 {
		t.Fatal("provider history missing")
	}
	var user llm.Message
	for _, message := range history {
		if message.Role == llm.RoleUser && message.FirstText() == "describe this" {
			user = message
			break
		}
	}
	if len(user.Blocks) != 2 || user.Blocks[0].Type != llm.BlockText || user.Blocks[1].Type != llm.BlockImage ||
		user.Blocks[1].Media == nil || user.Blocks[1].Media.ArtifactPath != media.ArtifactPath {
		t.Fatalf("provider user message = %+v", user)
	}
}

type pendingWebProvider struct {
	started chan struct{}
	release chan struct{}

	mu        sync.Mutex
	calls     int
	histories [][]llm.Message
}

func newPendingWebProvider() *pendingWebProvider {
	return &pendingWebProvider{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (p *pendingWebProvider) Name() string { return "web-pending-test" }

func (p *pendingWebProvider) Complete(ctx context.Context, sys string, h []llm.Message, t []llm.ToolSpec) (llm.Response, error) {
	p.mu.Lock()
	idx := p.calls
	p.calls++
	p.histories = append(p.histories, append([]llm.Message(nil), h...))
	p.mu.Unlock()
	if idx == 0 {
		close(p.started)
		select {
		case <-ctx.Done():
			return llm.Response{}, ctx.Err()
		case <-p.release:
		}
		return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "first"), StopReason: llm.StopEndTurn}, nil
	}
	return llm.Response{Message: llm.TextMessage(llm.RoleAssistant, "second"), StopReason: llm.StopEndTurn}, nil
}

func (p *pendingWebProvider) secondHistory() []llm.Message {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.histories) < 2 {
		return nil
	}
	return append([]llm.Message(nil), p.histories[1]...)
}

func TestWeb_CentralizedPendingInputLifecycle(t *testing.T) {
	work := t.TempDir()
	prov := newPendingWebProvider()
	cfg := config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work}
	srv := web.NewServer(web.Options{
		Cfg:      cfg,
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/threads", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		ID string `json:"thread_id"`
	}
	if err := json.NewDecoder(created.Body).Decode(&c); err != nil {
		t.Fatal(err)
	}
	created.Body.Close()

	start, err := http.Post(ts.URL+"/api/threads/"+c.ID+"/inputs", "application/json",
		strings.NewReader(`{"prompt":"hi"}`))
	if err != nil {
		t.Fatal(err)
	}
	if start.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(start.Body)
		start.Body.Close()
		t.Fatalf("start status = %d body=%s", start.StatusCode, body)
	}
	var startedBody struct {
		TurnID string `json:"turn_id"`
		Queued bool   `json:"queued"`
	}
	if err := json.NewDecoder(start.Body).Decode(&startedBody); err != nil {
		t.Fatal(err)
	}
	start.Body.Close()
	if startedBody.TurnID == "" || startedBody.Queued {
		t.Fatalf("started body = %+v, want Framework-owned start action", startedBody)
	}
	select {
	case <-prov.started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider did not start")
	}

	queued, err := http.Post(ts.URL+"/api/threads/"+c.ID+"/inputs", "application/json",
		strings.NewReader(`{"prompt":"steer now"}`))
	if err != nil {
		t.Fatal(err)
	}
	if queued.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(queued.Body)
		queued.Body.Close()
		t.Fatalf("queued status = %d body=%s", queued.StatusCode, body)
	}
	var queuedBody struct {
		Queued       bool `json:"queued"`
		PendingCount int  `json:"pending_count"`
	}
	if err := json.NewDecoder(queued.Body).Decode(&queuedBody); err != nil {
		t.Fatal(err)
	}
	queued.Body.Close()
	if !queuedBody.Queued || queuedBody.PendingCount != 1 {
		t.Fatalf("queued body = %+v", queuedBody)
	}
	close(prov.release)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		history := prov.secondHistory()
		if len(history) > 0 {
			if !historyContainsUserText(history, "steer now") {
				t.Fatalf("second provider call missing queued input: %+v", history)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(prov.secondHistory()) == 0 {
		t.Fatalf("pending input never reached second provider call\n%s",
			pendingInputFailureContext(filepath.Join(cfg.ThreadsDir(), c.ID)))
	}
	waitForWebTranscript(t, ts.URL, c.ID, startedBody.TurnID, 2*time.Second, "second assistant reply", func(messages []webTranscriptMessage) bool {
		for _, message := range messages {
			for _, block := range message.Blocks {
				if block.Type == "text" && block.Text == "second" {
					return true
				}
			}
		}
		return false
	})
}

func pendingInputFailureContext(threadDir string) string {
	journalPath := filepath.Join(threadDir, "journal.jsonl")
	var tail []byte
	file, err := os.Open(journalPath)
	if err == nil {
		defer file.Close()
		var info os.FileInfo
		info, err = file.Stat()
		if err == nil {
			offset := max(info.Size()-(16<<10), 0)
			tail = make([]byte, info.Size()-offset)
			var n int
			n, err = file.ReadAt(tail, offset)
			tail = tail[:n]
		}
	}
	stacks := make([]byte, 256<<10)
	n := runtime.Stack(stacks, true)
	return fmt.Sprintf("journal=%s journal_error=%v\nevents tail:\n%s\ngoroutines:\n%s",
		journalPath, err, tail, stacks[:n])
}

func TestPendingInputFailureContextIncludesJournalTail(t *testing.T) {
	threadDir := t.TempDir()
	terminal := `{"type":"turn.errored","payload":{"message":"injected durable write failure"}}`
	body := strings.Repeat("old diagnostic padding\n", 2048) + terminal + "\n"
	if err := os.WriteFile(filepath.Join(threadDir, "journal.jsonl"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	diagnostic := pendingInputFailureContext(threadDir)
	if !strings.Contains(diagnostic, terminal) || !strings.Contains(diagnostic, "goroutine ") {
		t.Fatalf("missing terminal event or goroutine context: %s", diagnostic)
	}
	if strings.Contains(diagnostic, body) {
		t.Fatal("failure context included the complete oversized journal")
	}
}

func TestPendingInputFailureContextReportsUnavailableJournal(t *testing.T) {
	diagnostic := pendingInputFailureContext(t.TempDir())
	if !strings.Contains(diagnostic, "journal_error=open ") || !strings.Contains(diagnostic, "goroutine ") {
		t.Fatalf("missing journal error or goroutine context: %s", diagnostic)
	}
}

func TestWeb_PendingInputQueuesDuringObservableTurn(t *testing.T) {
	work := t.TempDir()
	writeE2EObservableConfig(t, work)
	prov := newPendingWebProvider()
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	threadID := createWebMainThread(t, ts.URL)
	observables, err := http.Get(ts.URL + "/api/observables")
	if err != nil {
		t.Fatal(err)
	}
	observables.Body.Close()
	if observables.StatusCode != http.StatusOK {
		t.Fatalf("observable warmup status = %d", observables.StatusCode)
	}
	select {
	case <-prov.started:
	case <-time.After(10 * time.Second):
		t.Fatal("observable turn did not reach provider")
	}

	queued, err := http.Post(
		ts.URL+"/api/threads/"+threadID+"/inputs",
		"application/json",
		strings.NewReader(`{"prompt":"steer observable work"}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if queued.StatusCode != http.StatusAccepted {
		body, _ := io.ReadAll(queued.Body)
		queued.Body.Close()
		t.Fatalf("queued status = %d body=%s", queued.StatusCode, body)
	}
	var queuedBody struct {
		InputID      string `json:"input_id"`
		Queued       bool   `json:"queued"`
		PendingCount int    `json:"pending_count"`
	}
	if err := json.NewDecoder(queued.Body).Decode(&queuedBody); err != nil {
		queued.Body.Close()
		t.Fatal(err)
	}
	queued.Body.Close()
	if queuedBody.InputID == "" || !queuedBody.Queued || queuedBody.PendingCount != 1 {
		t.Fatalf("queued body = %+v", queuedBody)
	}

	close(prov.release)
	waitForCondition(t, 30*time.Second, func() bool {
		messages, err := fetchWebTranscript(&http.Client{Timeout: 2 * time.Second}, ts.URL, threadID)
		if err != nil {
			return false
		}
		inputCount := 0
		hasAssistant := false
		for _, message := range messages {
			for _, block := range message.Blocks {
				if block.Type != "text" {
					continue
				}
				if block.Text == "steer observable work" {
					inputCount++
				}
				if message.Role == "assistant" && block.Text == "second" {
					hasAssistant = true
				}
			}
		}
		return inputCount == 1 && hasAssistant
	})
	history := prov.secondHistory()
	if len(history) == 0 || !historyContainsUserText(history, "steer observable work") {
		t.Fatalf("second provider history = %+v", history)
	}
}

func TestWeb_ObservablesStartAndSurfaceObservation(t *testing.T) {
	work := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "agent")
	writeE2EObservableConfig(t, work)
	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "observable handled"), StopReason: llm.StopEndTurn},
	}}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work, AgentStateDir: stateDir},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	c := struct{ ID string }{ID: createWebMainThread(t, ts.URL)}

	var snapshot struct {
		Observables []observable.ObservableStatus `json:"observables"`
	}
	var records []observable.ObservationRecord
	waitForCondition(t, 5*time.Second, func() bool {
		resp, err := http.Get(ts.URL + "/api/observables")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var next struct {
			Observables []observable.ObservableStatus `json:"observables"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&next); err != nil {
			return false
		}
		snapshot = next
		if len(next.Observables) != 1 {
			return false
		}
		last := next.Observables[0].LastObservation
		if last.ID == "" || !strings.Contains(last.Content, "observable e2e payload") {
			return false
		}
		fetched, err := fetchObservableRecords(ts.URL, next.Observables[0].ID)
		if err != nil {
			return false
		}
		records = fetched
		return len(records) == 1 && records[0].State == observable.ObservationStateDelivered
	})
	got := snapshot.Observables[0]
	if got.ID != "observable-e2e" {
		t.Fatalf("observable id = %q", got.ID)
	}
	eventsData, err := os.ReadFile(filepath.Join(stateDir, "threads", c.ID, "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"type":"observable.started"`, `"type":"observation.delivered"`} {
		if !strings.Contains(string(eventsData), want) {
			t.Fatalf("events missing %s:\n%s", want, eventsData)
		}
	}
	var eventArtifactPath string
	waitForCondition(t, 5*time.Second, func() bool {
		messages, err := fetchWebTranscript(&http.Client{Timeout: 2 * time.Second}, ts.URL, c.ID)
		if err != nil {
			return false
		}
		for _, msg := range messages {
			for _, block := range msg.Blocks {
				if block.Type == "image" && block.Media != nil && strings.HasPrefix(block.Media.ArtifactPath, "event-media/") {
					eventArtifactPath = block.Media.ArtifactPath
					return true
				}
			}
		}
		return false
	})
	if err := os.Remove(filepath.Join(work, ".juex", "inbox", "observable-e2e.png")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(stateDir, "media", filepath.FromSlash(eventArtifactPath))); err != nil {
		t.Fatalf("stored observable event artifact unavailable after source removal: %v", err)
	}
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/observables/observable-e2e", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete command observable status=%d body=%s", resp.StatusCode, body)
	}
}

func TestWeb_CreateScheduleObservableAndControlLifecycle(t *testing.T) {
	work := t.TempDir()
	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "schedule handled"), StopReason: llm.StopEndTurn},
	}}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	_ = createWebMainThread(t, ts.URL)

	scheduledAt := time.Now().UTC().Add(time.Hour)
	body, err := json.Marshal(map[string]any{
		"id":   "schedule-e2e",
		"type": "schedule",
		"schedule_config": map[string]any{
			"once": map[string]any{
				"at": scheduledAt.Format(time.RFC3339Nano),
			},
			"observation": map[string]any{
				"kind":     "heartbeat",
				"severity": "info",
				"content":  "schedule e2e payload",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/api/observables", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create schedule status=%d body=%s", resp.StatusCode, respBody)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/api/observables/schedule-e2e/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var stopped observable.ObservableStatus
	if err := json.NewDecoder(resp.Body).Decode(&stopped); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || stopped.State != observable.RunStateStopped {
		t.Fatalf("stop schedule status=%d body=%+v", resp.StatusCode, stopped)
	}

	resp, err = http.Post(ts.URL+"/api/observables/schedule-e2e/start", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var restarted observable.ObservableStatus
	if err := json.NewDecoder(resp.Body).Decode(&restarted); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || restarted.State != observable.RunStateRunning {
		t.Fatalf("restart schedule status=%d body=%+v", resp.StatusCode, restarted)
	}

	resp, err = http.Get(ts.URL + "/api/observables")
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Observables []observable.ObservableStatus `json:"observables"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || len(snapshot.Observables) != 1 {
		t.Fatalf("list schedules status=%d body=%+v", resp.StatusCode, snapshot)
	}
	if got := snapshot.Observables[0]; got.SourceType != observable.SourceTypeSchedule || got.State != observable.RunStateRunning ||
		got.Schedule == nil || got.Schedule.NextOccurrence == nil || !got.Schedule.NextOccurrence.Equal(scheduledAt) {
		t.Fatalf("schedule status = %+v", got)
	} else if got.ScheduleConfig == nil || got.ScheduleConfig.Observation.Content != "schedule e2e payload" {
		t.Fatalf("schedule config = %+v, want list-visible observation content", got.ScheduleConfig)
	}
	req, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/observables/schedule-e2e", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("delete schedule status=%d body=%s", resp.StatusCode, respBody)
	}
}

func TestWeb_ScheduleCatchUpAutomaticallySurfacesObservation(t *testing.T) {
	work := t.TempDir()
	stateDir := filepath.Join(t.TempDir(), "agent")
	cfg := config.Config{
		ProviderID:    "openai",
		APIKey:        "x",
		Model:         "m",
		WorkDir:       work,
		AgentStateDir: stateDir,
	}
	scheduledAt := time.Now().UTC().Add(-time.Minute)
	body, err := json.Marshal(map[string]any{
		"observables": []any{
			map[string]any{
				"id":   "schedule-auto-e2e",
				"type": "schedule",
				"schedule_config": map[string]any{
					"once": map[string]any{"at": scheduledAt.Format(time.RFC3339Nano)},
					"catch_up": map[string]any{
						"mode":                 "latest",
						"max_lateness_minutes": 1440,
					},
					"observation": map[string]any{
						"kind":     "heartbeat",
						"severity": "info",
						"content":  "automatic schedule e2e payload",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cfg.ObservablesConfigPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.ObservablesConfigPath(), append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	store := observable.NewStore(cfg.ObservablesStateDir(), observable.StoreOptions{})
	if err := store.RecordScheduleState(observable.ScheduleStateRecord{
		ObservableID:    "schedule-auto-e2e",
		LastEvaluatedAt: scheduledAt.Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}

	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "schedule handled"), StopReason: llm.StopEndTurn},
	}}
	srv := web.NewServer(web.Options{Cfg: cfg, Provider: prov})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	threadInfo := struct{ ID string }{ID: createWebMainThread(t, ts.URL)}

	var status *observable.ObservableStatus
	var records []observable.ObservationRecord
	var eventsData []byte
	waitForCondition(t, 5*time.Second, func() bool {
		resp, err := http.Get(ts.URL + "/api/observables")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var snapshot struct {
			Observables []observable.ObservableStatus `json:"observables"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&snapshot) != nil {
			return false
		}
		status = observableStatusByID(snapshot.Observables, "schedule-auto-e2e")
		if status == nil || status.LastObservation.Content != "automatic schedule e2e payload" {
			return false
		}
		records, err = fetchObservableRecords(ts.URL, "schedule-auto-e2e")
		if err != nil || !scheduleDeliveryVisible(status, records) {
			return false
		}
		// Delivered store snapshots alone do not prove journal publication or
		// Provider execution.
		eventsData, err = os.ReadFile(filepath.Join(stateDir, "threads", threadInfo.ID, "journal.jsonl"))
		return err == nil && strings.Contains(string(eventsData), `"type":"observation.delivered"`) &&
			strings.Contains(messagesText(prov.history(0)), "automatic schedule e2e payload")
	})
	if status.LastObservation.State != observable.ObservationStateDelivered {
		t.Fatalf("last observation = %+v", status.LastObservation)
	}
	if !strings.Contains(string(eventsData), `"type":"observation.delivered"`) {
		t.Fatalf("events missing automatic observation delivery:\n%s", eventsData)
	}
	if got := messagesText(prov.history(0)); !strings.Contains(got, "automatic schedule e2e payload") {
		t.Fatalf("Provider did not receive the automatic observation:\n%s", got)
	}
}

func scheduleDeliveryVisible(status *observable.ObservableStatus, records []observable.ObservationRecord) bool {
	// The status and record list are separate HTTP snapshots, not one read.
	return status != nil && status.LastObservation.State == observable.ObservationStateDelivered &&
		len(records) == 1 && records[0].State == observable.ObservationStateDelivered
}

func TestScheduleDeliveryVisibleRequiresBothSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name        string
		statusState string
		recordState string
		want        bool
	}{
		{name: "both recorded", statusState: observable.ObservationStateRecorded, recordState: observable.ObservationStateRecorded},
		{name: "delivery between reads", statusState: observable.ObservationStateRecorded, recordState: observable.ObservationStateDelivered},
		{name: "record not delivered", statusState: observable.ObservationStateDelivered, recordState: observable.ObservationStateQueued},
		{name: "both delivered", statusState: observable.ObservationStateDelivered, recordState: observable.ObservationStateDelivered, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := &observable.ObservableStatus{LastObservation: observable.ObservationRecord{State: tc.statusState}}
			records := []observable.ObservationRecord{{State: tc.recordState}}
			if got := scheduleDeliveryVisible(status, records); got != tc.want {
				t.Fatalf("delivery visible = %t, want %t for status=%s record=%s", got, tc.want, tc.statusState, tc.recordState)
			}
		})
	}
}

func TestWeb_RunMonthlyScheduleObservableOnce(t *testing.T) {
	work := t.TempDir()
	prov := &webProvider{steps: []llm.Response{
		{Message: llm.TextMessage(llm.RoleAssistant, "manual schedule handled"), StopReason: llm.StopEndTurn},
	}}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: prov,
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	created, err := http.Post(ts.URL+"/api/threads", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if created.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(created.Body)
		created.Body.Close()
		t.Fatalf("create Thread status=%d body=%s", created.StatusCode, body)
	}
	created.Body.Close()

	body, err := json.Marshal(map[string]any{
		"id":   "manual-schedule-e2e",
		"type": "schedule",
		"schedule_config": map[string]any{
			"timezone": "Asia/Shanghai",
			"monthly": map[string]any{
				"days":  []int{1, 15, 31},
				"times": []string{"09:00", "17:30"},
			},
			"observation": map[string]any{
				"kind":     "heartbeat",
				"severity": "info",
				"content":  "manual schedule e2e payload",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(ts.URL+"/api/observables", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create schedule status=%d body=%s", resp.StatusCode, respBody)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/api/observables/manual-schedule-e2e/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("stop schedule status=%d body=%s", resp.StatusCode, respBody)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/api/observables/manual-schedule-e2e/run", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var record observable.ObservationRecord
	if err := json.NewDecoder(resp.Body).Decode(&record); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("run schedule status=%d body=%+v", resp.StatusCode, record)
	}
	if !strings.HasPrefix(record.SourceEventID, "schedule:manual-schedule-e2e:manual:") {
		t.Fatalf("manual source event id = %q", record.SourceEventID)
	}
	if record.RunID != "" {
		t.Fatalf("manual run id = %q, want empty", record.RunID)
	}

	var delivered observable.ObservationRecord
	waitForCondition(t, 5*time.Second, func() bool {
		records, err := fetchObservableRecords(ts.URL, "manual-schedule-e2e")
		if err != nil {
			return false
		}
		for _, candidate := range records {
			if candidate.SourceEventID == record.SourceEventID {
				delivered = candidate
				return candidate.State == observable.ObservationStateDelivered
			}
		}
		return false
	})
	if delivered.Content != "manual schedule e2e payload" {
		t.Fatalf("manual content = %q", delivered.Content)
	}

	resp, err = http.Get(ts.URL + "/api/observables")
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Observables []observable.ObservableStatus `json:"observables"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	status := observableStatusByID(list.Observables, "manual-schedule-e2e")
	if status == nil || status.State != observable.RunStateStopped {
		t.Fatalf("schedule status after run = %+v, want stopped", status)
	}
	if status.ScheduleConfig == nil || status.ScheduleConfig.Monthly == nil ||
		status.Schedule == nil || status.Schedule.Summary != "monthly days 1,15,31 at 09:00,17:30 Asia/Shanghai" ||
		status.Schedule.NextOccurrence == nil {
		t.Fatalf("monthly schedule status = %+v", status)
	}
	if status.LastObservation.SourceEventID != record.SourceEventID {
		t.Fatalf("last observation = %+v, want %q", status.LastObservation, record.SourceEventID)
	}

	resp, err = http.Post(ts.URL+"/api/observables/missing/run", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("run missing status=%d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	commandBody, err := json.Marshal(map[string]any{
		"id":   "manual-command-e2e",
		"type": "command",
		"command_config": map[string]any{
			"command": os.Args[0],
			"args":    []string{"-test.run=TestE2EQuietObservableHelperProcess"},
			"env":     map[string]string{"JUEX_E2E_QUIET_OBSERVABLE": "1"},
			"streams": []string{"stdout"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err = http.Post(ts.URL+"/api/observables", "application/json", bytes.NewReader(commandBody))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create command status=%d body=%s", resp.StatusCode, respBody)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/api/observables/manual-command-e2e/run", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("run command status=%d, want %d", resp.StatusCode, http.StatusConflict)
	}
}

func TestWeb_OldObservableShapeIsVisibleAndBlocksTaggedEdits(t *testing.T) {
	work := t.TempDir()
	configBody := `{"observables":[` +
		`{"id":"invalid-command","command":"echo"},` +
		`{"id":"valid-schedule","type":"schedule","schedule_config":{"interval":{"every_seconds":3600},"observation":{"content":"valid sibling"}}}` +
		`]}`
	configPath := filepath.Join(work, ".juex", "observables.json")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(configBody), 0o644); err != nil {
		t.Fatal(err)
	}
	srv := web.NewServer(web.Options{
		Cfg:      config.Config{ProviderID: "openai", APIKey: "x", Model: "m", WorkDir: work},
		Provider: &webProvider{},
	})
	t.Cleanup(srv.Close)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	var statuses []observable.ObservableStatus
	waitForCondition(t, 5*time.Second, func() bool {
		resp, err := http.Get(ts.URL + "/api/observables")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		var body struct {
			Observables []observable.ObservableStatus `json:"observables"`
		}
		if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil {
			return false
		}
		statuses = body.Observables
		valid, invalid := observableStatusByID(statuses, "valid-schedule"), observableStatusByConfigError(statuses, "invalid-command")
		return valid != nil && valid.State == observable.RunStateRunning && invalid != nil && invalid.State == observable.RunStateErrored
	})
	invalid := observableStatusByConfigError(statuses, "invalid-command")
	if invalid == nil || !strings.Contains(invalid.LastError, "type plus command_config") {
		t.Fatalf("invalid config issue = %+v, want rewrite hint", invalid)
	}

	createBody := strings.NewReader(`{"id":"blocked-command","type":"command","command_config":{"command":"echo"}}`)
	resp, err := http.Post(ts.URL+"/api/observables", "application/json", createBody)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(responseBody), "fix invalid entries before editing") {
		t.Fatalf("blocked edit status=%d body=%s", resp.StatusCode, responseBody)
	}
}

func observableStatusByID(statuses []observable.ObservableStatus, id string) *observable.ObservableStatus {
	for i := range statuses {
		if statuses[i].ID == id {
			return &statuses[i]
		}
	}
	return nil
}

func observableStatusByConfigError(statuses []observable.ObservableStatus, id string) *observable.ObservableStatus {
	for index := range statuses {
		if strings.Contains(statuses[index].LastError, id) {
			return &statuses[index]
		}
	}
	return nil
}

type webStartTurnResponse struct {
	TurnID string `json:"turn_id"`
}

type webTranscriptMessage struct {
	Kind   string `json:"kind"`
	Role   string `json:"role"`
	Blocks []struct {
		Type  string        `json:"type"`
		Text  string        `json:"text"`
		Media *llm.MediaRef `json:"media,omitempty"`
	} `json:"blocks"`
}

func waitForWebTranscript(t *testing.T, baseURL, threadID, turnID string, timeout time.Duration, label string, match func([]webTranscriptMessage) bool) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(timeout)
	var lastErr, lastState string
	var lastMessages []webTranscriptMessage
	for time.Now().Before(deadline) {
		matched := false
		messages, err := fetchWebTranscript(client, baseURL, threadID)
		if err != nil {
			lastErr = err.Error()
		} else {
			lastMessages = messages
			matched = match(messages)
		}
		state, turnErr, err := fetchWebTurnState(client, baseURL, threadID, turnID)
		if err != nil {
			lastState = err.Error()
		} else {
			lastState = state
			if state == "errored" {
				t.Fatalf("turn %s errored while waiting for %s: %s", turnID, label, turnErr)
			}
			if matched && state == "completed" {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last_state=%q last_error=%q last_messages=%+v", label, lastState, lastErr, lastMessages)
}

func waitForCondition(t *testing.T, timeout time.Duration, match func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if match() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func fetchWebTranscript(client *http.Client, baseURL, threadID string) ([]webTranscriptMessage, error) {
	resp, err := client.Get(baseURL + "/api/threads/" + threadID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("Thread status=%d body=%s", resp.StatusCode, body)
	}
	var parsed struct {
		Items []struct {
			Message *webTranscriptMessage `json:"message"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	messages := make([]webTranscriptMessage, 0, len(parsed.Items))
	for _, item := range parsed.Items {
		if item.Message != nil {
			messages = append(messages, *item.Message)
		}
	}
	return messages, nil
}

func createWebMainThread(t *testing.T, baseURL string) string {
	t.Helper()
	created, err := http.Get(baseURL + "/api/threads")
	if err != nil {
		t.Fatal(err)
	}
	defer created.Body.Close()
	if created.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(created.Body)
		t.Fatalf("create Thread status = %d body=%s", created.StatusCode, body)
	}
	return "0"
}

func historyContainsUserText(history []llm.Message, text string) bool {
	for _, message := range history {
		if message.Role == llm.RoleUser && message.FirstText() == text {
			return true
		}
	}
	return false
}

func uploadWebThreadImage(t *testing.T, baseURL, threadID string) llm.MediaRef {
	t.Helper()
	resp := postWebThreadAttachment(t, baseURL, threadID, "screen.png", webUploadPNG(t))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload status = %d body=%s", resp.StatusCode, body)
	}
	var ref llm.MediaRef
	if err := json.NewDecoder(resp.Body).Decode(&ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func postWebThreadAttachment(t *testing.T, baseURL, threadID, filename string, data []byte) *http.Response {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/threads/"+threadID+"/attachments", &body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func webUploadPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func fetchObservableRecords(baseURL, id string) ([]observable.ObservationRecord, error) {
	resp, err := http.Get(baseURL + "/api/observables/" + id + "/observations")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("observations status=%d body=%s", resp.StatusCode, body)
	}
	var body struct {
		Observations []observable.ObservationRecord `json:"observations"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	return body.Observations, nil
}

func writeE2EObservableConfig(t *testing.T, work string) {
	t.Helper()
	attachmentPath := ".juex/inbox/observable-e2e.png"
	writeE2ETestPNG(t, filepath.Join(work, filepath.FromSlash(attachmentPath)))
	cfg := map[string]any{
		"observables": []map[string]any{
			{
				"id":   "observable-e2e",
				"type": "command",
				"command_config": map[string]any{
					"command": os.Args[0],
					"args":    []string{"-test.run=TestE2EObservableHelperProcess"},
					"env": map[string]string{
						"JUEX_E2E_OBSERVABLE":            "1",
						"JUEX_E2E_OBSERVABLE_ATTACHMENT": attachmentPath,
					},
					"streams": []string{"stdout"},
					"parser": map[string]string{
						"type":              "jsonl",
						"content_field":     "content",
						"kind_field":        "type",
						"severity_field":    "level",
						"attachments_field": "attachments",
					},
					"batch": map[string]int{
						"interval_seconds": 5,
						"max_chars":        1000,
					},
				},
			},
		},
	}
	body, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(work, ".juex", "observables.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestE2EObservableHelperProcess(t *testing.T) {
	if os.Getenv("JUEX_E2E_OBSERVABLE") != "1" {
		return
	}
	attachment := os.Getenv("JUEX_E2E_OBSERVABLE_ATTACHMENT")
	_, _ = os.Stdout.WriteString(`{"type":"e2e_event","level":"info","content":"observable e2e payload","attachments":[{"path":"` + attachment + `","media_type":"image/png"}]}` + "\n")
	os.Exit(0)
}

func TestE2EQuietObservableHelperProcess(t *testing.T) {
	if os.Getenv("JUEX_E2E_QUIET_OBSERVABLE") != "1" {
		return
	}
	os.Exit(0)
}

func writeE2ETestPNG(t *testing.T, path string) {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+/p9sAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func fetchWebTurnState(client *http.Client, baseURL, threadID, turnID string) (string, string, error) {
	resp, err := client.Get(baseURL + "/api/threads/" + threadID + "/status")
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", "", fmt.Errorf("turn status=%d body=%s", resp.StatusCode, body)
	}
	var snapshot juexruntime.StatusSnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return "", "", err
	}
	if snapshot.Turn == nil {
		return "", "", fmt.Errorf("status has no turn for %s", turnID)
	}
	if snapshot.Turn.ID != turnID {
		return "", "", fmt.Errorf("status turn = %s, want %s", snapshot.Turn.ID, turnID)
	}
	turnErr := ""
	if snapshot.Turn.Error != nil {
		turnErr = snapshot.Turn.Error.Message
	}
	return string(snapshot.Turn.State), turnErr, nil
}
