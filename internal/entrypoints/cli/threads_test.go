package cli

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/config"
	"github.com/juex-ai/juex/internal/app/modulecatalog"
	"github.com/juex-ai/juex/internal/entrypoints/agenthttp"
	"github.com/juex-ai/juex/internal/framework/endpoint"
	"github.com/juex-ai/juex/internal/framework/thread"
)

func TestThreadClientHonorsServerWorkerDepth(t *testing.T) {
	for _, maxDepth := range []int{1, 2} {
		t.Run(fmt.Sprint(maxDepth), func(t *testing.T) {
			server := agenthttp.NewServer(agenthttp.Options{Cfg: config.Config{ModuleInventory: modulecatalog.Inventory(), Preset: config.PresetMinimal, WorkDir: t.TempDir(), AgentStateDir: t.TempDir(), WorkerThreadMaxDepth: maxDepth}})
			defer server.Close()
			httpServer := httptest.NewServer(server.APIHandler())
			defer httpServer.Close()
			target, err := endpoint.Parse(strings.Replace(httpServer.URL, "http://", "tcp://", 1))
			if err != nil {
				t.Fatal(err)
			}
			transport := target.NewTransport()
			defer transport.CloseIdleConnections()
			client := &agentClient{target: target, client: &http.Client{Transport: transport}}
			parentID := "0"
			for depth := 1; depth <= maxDepth; depth++ {
				alias := fmt.Sprintf("worker-%d", depth)
				var created thread.Info
				if err := client.doJSON(t.Context(), http.MethodPost, "/api/threads", map[string]string{"parent_thread_id": parentID, "alias": alias}, &created); err != nil {
					t.Fatal(err)
				}
				resolved, err := client.resolveThread(t.Context(), alias, false)
				if err != nil || resolved.ThreadID != created.ID {
					t.Fatalf("resolve parent alias: %+v, %v", resolved, err)
				}
				parentID = resolved.ThreadID
			}
			err = client.doJSON(t.Context(), http.MethodPost, "/api/threads", map[string]string{"parent_thread_id": parentID}, nil)
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("max_depth=%d", maxDepth)) {
				t.Fatalf("CLI client error = %v", err)
			}
		})
	}
}

func TestAgentClientResolvesThreadIDAndAliasAcrossIndexSections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/threads" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"active_threads":[{"thread_id":"0","alias":"main"},{"thread_id":"abc123","alias":"reviewer"}],"archived_threads":[{"thread_id":"def456","alias":"old"}]}`)
	}))
	defer server.Close()
	target, err := endpoint.Parse(strings.Replace(server.URL, "http://", "tcp://", 1))
	if err != nil {
		t.Fatal(err)
	}
	transport := target.NewTransport()
	defer transport.CloseIdleConnections()
	client := &agentClient{target: target, client: &http.Client{Transport: transport}}
	if got, err := client.resolveThread(context.Background(), "reviewer", false); err != nil || got.ThreadID != "abc123" {
		t.Fatalf("active alias = %+v, err=%v", got, err)
	}
	if _, err := client.resolveThread(context.Background(), "old", false); err == nil {
		t.Fatal("archived Thread unexpectedly resolved without includeArchived")
	}
	if got, err := client.resolveThread(context.Background(), "old", true); err != nil || got.ThreadID != "def456" {
		t.Fatalf("archived alias = %+v, err=%v", got, err)
	}
}

func TestRenderThreadsTableSeparatesRetentionAndExecutionState(t *testing.T) {
	var output bytes.Buffer
	cmd := newThreadsListCmd(&agentSelectorFlags{})
	cmd.SetOut(&output)
	renderThreadsTable(cmd, []thread.IndexEntry{
		{ThreadID: "abc123", Alias: "active", RetentionState: thread.RetentionActive, ExecutionState: thread.ExecutionFailed},
		{ThreadID: "def456", Alias: "archived", RetentionState: thread.RetentionArchived},
	})

	text := output.String()
	if !strings.Contains(text, "RETENTION") || !strings.Contains(text, "EXECUTION") {
		t.Fatalf("table header does not expose both lifecycle axes:\n%s", text)
	}
	if !strings.Contains(text, "active     failed") || !strings.Contains(text, "archived   -") {
		t.Fatalf("table rows do not preserve lifecycle meaning:\n%s", text)
	}
}

func TestAgentClientRenamesArchivedThread(t *testing.T) {
	patched := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/threads":
			fmt.Fprint(w, `{"active_threads":[{"thread_id":"0","alias":"main"}],"archived_threads":[{"thread_id":"def456","alias":"old"}]}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/threads/def456":
			patched = true
			fmt.Fprint(w, `{"thread_id":"def456","alias":"renamed"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	target, err := endpoint.Parse(strings.Replace(server.URL, "http://", "tcp://", 1))
	if err != nil {
		t.Fatal(err)
	}
	transport := target.NewTransport()
	defer transport.CloseIdleConnections()
	client := &agentClient{target: target, client: &http.Client{Transport: transport}}

	result, err := client.renameThread(context.Background(), "old", "renamed")
	if err != nil {
		t.Fatal(err)
	}
	if !patched || result.ID != "def456" || result.Alias != "renamed" {
		t.Fatalf("archived rename result = %+v, patched=%t", result, patched)
	}
}

func TestRootExposesSendAndThreadManagement(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{{"agent", "send"}, {"thread", "send"}, {"thread", "list"}, {"thread", "archive"}, {"thread", "delete"}} {
		command, _, err := root.Find(path)
		if err != nil || command == nil {
			t.Fatalf("command %v missing: %v", path, err)
		}
	}
}

func TestThreadsCreateExposesExplicitParentSelector(t *testing.T) {
	root := newRootCmd()
	command, _, err := root.Find([]string{"thread", "create"})
	if err != nil {
		t.Fatal(err)
	}
	flag := command.Flags().Lookup("parent")
	if flag == nil || flag.DefValue != "0" {
		t.Fatalf("--parent flag = %+v", flag)
	}
}

func TestRenderThreadShowReportsCurrentGenerationJournal(t *testing.T) {
	info := thread.Info{
		ID: "abc123", Alias: "worker", Dir: "/state/threads/abc123",
		RetentionState: thread.RetentionActive, ExecutionState: thread.ExecutionIdle,
		GenerationID: "g000002", GenerationJournalPath: "/state/threads/abc123/generations/g000002.jsonl",
		CreatedAt:      thread.NewTimestamp(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)),
		LastActivityAt: thread.NewTimestamp(time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)),
	}
	for _, jsonOut := range []bool{false, true} {
		var output bytes.Buffer
		cmd := newThreadsShowCmd(&agentSelectorFlags{})
		cmd.SetOut(&output)
		renderThreadShow(cmd, info, jsonOut)
		if !strings.Contains(output.String(), "generation_journal") || !strings.Contains(output.String(), info.GenerationJournalPath) {
			t.Fatalf("json=%t output omitted current Generation Journal:\n%s", jsonOut, output.String())
		}
	}
}
