package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestResourceEventHubClassifiesWorkspaceAndRuntimePaths(t *testing.T) {
	workDir := t.TempDir()
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	hub := newResourceEventHub(workDir, sessionsDir)

	tests := []struct {
		path string
		want string
	}{
		{path: filepath.Join(workDir, "src", "main.go"), want: resourceWorkspace},
		{path: filepath.Join(workDir, ".git", "index")},
		{path: filepath.Join(workDir, ".juex", "events.jsonl")},
		{path: filepath.Join(workDir, ".juex", "observables.json"), want: resourceObservable},
		{path: filepath.Join(sessionsDir, "session-1", "scratchpad", "notes.md"), want: resourceScratchpad},
	}
	for _, test := range tests {
		if got := hub.resourceForPath(test.path); got != test.want {
			t.Errorf("resourceForPath(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestResourceEventHubProjectsObservableAndWriteEvents(t *testing.T) {
	hub := newResourceEventHub(t.TempDir(), t.TempDir())
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	hub.Publish(events.Event{Type: observable.EventObservableStarted})
	hub.Publish(events.Event{
		Type:    toolevents.CompletedType,
		Payload: toolevents.CompletedPayload{Name: "write"},
	})

	select {
	case <-subscription.updates:
	case <-time.After(2 * time.Second):
		t.Fatal("resource subscriber was not notified")
	}
	got := subscription.take().Resources
	want := []string{resourceObservable, resourceScratchpad, resourceWorkspace}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resources = %v, want %v", got, want)
	}
}

func TestResourceEventHubCoalescesProjectedAndFilesystemChanges(t *testing.T) {
	workDir := t.TempDir()
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hub := newResourceEventHub(workDir, sessionsDir)
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	if err := os.WriteFile(filepath.Join(workDir, "created.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	hub.Publish(events.Event{
		Type:    toolevents.CompletedType,
		Payload: toolevents.CompletedPayload{Name: "write"},
	})

	select {
	case <-subscription.updates:
		got := subscription.take().Resources
		want := []string{resourceScratchpad, resourceWorkspace}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("resources = %v, want %v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("coalesced resource mutation was not observed")
	}
	select {
	case <-subscription.updates:
		t.Fatalf("duplicate resource event = %+v", subscription.take())
	case <-time.After(2 * resourceChangeDebounce):
	}
}

func TestResourceEventHubWatchesWorkspaceOnDemand(t *testing.T) {
	workDir := t.TempDir()
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hub := newResourceEventHub(workDir, sessionsDir)
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	if err := os.WriteFile(filepath.Join(workDir, "created.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-subscription.updates:
		got := subscription.take().Resources
		if !reflect.DeepEqual(got, []string{resourceWorkspace}) {
			t.Fatalf("resources = %v", got)
		}
	case <-ctx.Done():
		t.Fatal("workspace mutation was not observed")
	}
}

func TestResourceEventHubWatchesLateObservableConfigWithoutRuntimeTree(t *testing.T) {
	workDir := t.TempDir()
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hub := newResourceEventHub(workDir, sessionsDir)
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	configDir := filepath.Join(workDir, ".juex")
	if err := os.Mkdir(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "observables.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-subscription.updates:
		got := subscription.take().Resources
		if !reflect.DeepEqual(got, []string{resourceObservable}) {
			t.Fatalf("resources = %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late observable config mutation was not observed")
	}
}

func TestResourceEventsEndpointStartsWithAuthoritativeInvalidation(t *testing.T) {
	server := NewServer(Options{})
	httpServer := httptest.NewServer(server.APIHandler())
	t.Cleanup(func() {
		httpServer.Close()
		server.Close()
	})

	response, err := http.Get(httpServer.URL + "/api/resource-events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}
	line, err := bufio.NewReader(response.Body).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	var event agentResourceEvent
	if err := json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &event); err != nil {
		t.Fatal(err)
	}
	want := []string{resourceObservable, resourceRuntime, resourceScratchpad, resourceWorkspace}
	if event.Type != "resource.changed" || !reflect.DeepEqual(event.Resources, want) {
		t.Fatalf("event = %+v, want resources %v", event, want)
	}
}
