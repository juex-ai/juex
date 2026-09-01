package cli

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/endpoint"
)

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

func TestRootExposesSendAndThreadManagement(t *testing.T) {
	root := newRootCmd()
	for _, path := range [][]string{{"send"}, {"threads", "list"}, {"threads", "archive"}, {"threads", "delete"}} {
		command, _, err := root.Find(path)
		if err != nil || command == nil {
			t.Fatalf("command %v missing: %v", path, err)
		}
	}
}

func TestThreadsCreateExposesExplicitParentSelector(t *testing.T) {
	root := newRootCmd()
	command, _, err := root.Find([]string{"threads", "create"})
	if err != nil {
		t.Fatal(err)
	}
	flag := command.Flags().Lookup("parent")
	if flag == nil || flag.DefValue != "0" {
		t.Fatalf("--parent flag = %+v", flag)
	}
}
