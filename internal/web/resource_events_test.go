package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
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

func TestResourceEventHubClassifiesMutableRuntimeInputs(t *testing.T) {
	workDir := t.TempDir()
	hub := newResourceEventHub(workDir, t.TempDir())
	tests := []struct {
		path string
		want []string
	}{
		{path: filepath.Join(workDir, "AGENTS.md"), want: []string{resourceWorkspace, resourceRuntime}},
		{path: filepath.Join(workDir, ".agents"), want: []string{resourceWorkspace, resourceRuntime}},
		{path: filepath.Join(workDir, ".agents", "AGENTS.md"), want: []string{resourceWorkspace, resourceRuntime}},
		{path: filepath.Join(workDir, ".agents", "skills", "review", "SKILL.md"), want: []string{resourceWorkspace}},
		{path: filepath.Join(workDir, ".agents", "mcp.json"), want: []string{resourceWorkspace}},
		{path: filepath.Join(workDir, "src", "main.go"), want: []string{resourceWorkspace}},
	}
	for _, test := range tests {
		if got := hub.resourcesForPath(test.path); !reflect.DeepEqual(got, test.want) {
			t.Errorf("resourcesForPath(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}

func TestResourceEventHubClassifiesExternalRuntimeInputs(t *testing.T) {
	workDir := t.TempDir()
	globalAgentsDir := t.TempDir()
	memoryDir := filepath.Join(t.TempDir(), "memory")
	hub := newResourceEventHub(workDir, t.TempDir())
	hub.setRuntimeInputs(filepath.Join(globalAgentsDir, "AGENTS.md"), memoryDir)

	tests := []struct {
		path string
		want []string
	}{
		{path: globalAgentsDir, want: []string{resourceRuntime}},
		{path: filepath.Join(globalAgentsDir, "AGENTS.md"), want: []string{resourceRuntime}},
		{path: memoryDir, want: []string{resourceRuntime}},
		{path: filepath.Join(memoryDir, "facts.md"), want: []string{resourceRuntime}},
		{path: filepath.Join(globalAgentsDir, "skills", "demo", "SKILL.md")},
	}
	for _, test := range tests {
		if got := hub.resourcesForPath(test.path); !reflect.DeepEqual(got, test.want) {
			t.Errorf("resourcesForPath(%q) = %v, want %v", test.path, got, test.want)
		}
	}
}

func TestResourceEventHubProjectsMemoryToolEvents(t *testing.T) {
	hub := newResourceEventHub(t.TempDir(), t.TempDir())
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	hub.Publish(events.Event{
		Type:    toolevents.CompletedType,
		Payload: toolevents.CompletedPayload{Name: "memory_write"},
	})
	select {
	case <-subscription.updates:
		if got := subscription.take().Resources; !reflect.DeepEqual(got, []string{resourceRuntime}) {
			t.Fatalf("resources = %v, want runtime", got)
		}
	case <-time.After(time.Second):
		t.Fatal("memory tool event did not invalidate runtime")
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

func TestResourceEventHubWatchesMutableRuntimeInput(t *testing.T) {
	workDir := t.TempDir()
	hub := newResourceEventHub(workDir, t.TempDir())
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	if err := os.WriteFile(filepath.Join(workDir, "AGENTS.md"), []byte("updated guidance"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-subscription.updates:
		got := subscription.take().Resources
		want := []string{resourceRuntime, resourceWorkspace}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("resources = %v, want %v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runtime input mutation was not observed")
	}
}

func TestResourceEventHubWatchesLateAgentsDirectory(t *testing.T) {
	workDir := t.TempDir()
	hub := newResourceEventHub(workDir, t.TempDir())
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	if err := os.Mkdir(filepath.Join(workDir, ".agents"), 0o755); err != nil {
		t.Fatal(err)
	}
	select {
	case <-subscription.updates:
		got := subscription.take().Resources
		want := []string{resourceRuntime, resourceWorkspace}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("resources = %v, want %v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late .agents directory was not observed as a runtime input")
	}
}

func TestResourceEventHubWatchesExternalGlobalAgentsFile(t *testing.T) {
	globalAgentsDir := t.TempDir()
	hub := newResourceEventHub(t.TempDir(), t.TempDir())
	hub.setRuntimeInputs(filepath.Join(globalAgentsDir, "AGENTS.md"), "")
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	if err := os.WriteFile(filepath.Join(globalAgentsDir, "AGENTS.md"), []byte("global guidance"), 0o644); err != nil {
		t.Fatal(err)
	}
	select {
	case <-subscription.updates:
		if got := subscription.take().Resources; !reflect.DeepEqual(got, []string{resourceRuntime}) {
			t.Fatalf("resources = %v, want runtime", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("external global AGENTS.md mutation was not observed")
	}
}

func TestResourceEventHubWatchesLateMemoryDirectory(t *testing.T) {
	memoryDir := filepath.Join(t.TempDir(), "memory")
	hub := newResourceEventHub(t.TempDir(), t.TempDir())
	hub.setRuntimeInputs("", memoryDir)
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	if err := os.Mkdir(memoryDir, 0o755); err != nil {
		t.Fatal(err)
	}
	select {
	case <-subscription.updates:
		if got := subscription.take().Resources; !reflect.DeepEqual(got, []string{resourceRuntime}) {
			t.Fatalf("resources = %v, want runtime", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("late memory directory was not observed")
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

func TestResourceEventHubRejectsIncompleteInitialWatchTree(t *testing.T) {
	workDir := t.TempDir()
	hub := newResourceEventHub(workDir, t.TempDir())
	hub.addWatch = func(_ *fsnotify.Watcher, path string) error {
		if filepath.Clean(path) == filepath.Clean(workDir) {
			return errors.New("watch limit reached")
		}
		return nil
	}

	if _, err := hub.subscribe(); err == nil || !strings.Contains(err.Error(), "watch limit reached") {
		t.Fatalf("subscribe error = %v, want watch registration failure", err)
	}
	if hub.watcher != nil || len(hub.subscribers) != 0 {
		t.Fatalf("failed watcher remained active: watcher=%v subscribers=%d", hub.watcher, len(hub.subscribers))
	}
}

func TestResourceEventHubEndsStreamWhenLateDirectoryCannotBeWatched(t *testing.T) {
	workDir := t.TempDir()
	hub := newResourceEventHub(workDir, t.TempDir())
	hub.addWatch = func(watcher *fsnotify.Watcher, path string) error {
		if strings.HasPrefix(filepath.Clean(path), filepath.Join(workDir, "late")) {
			return errors.New("watch limit reached")
		}
		return watcher.Add(path)
	}
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()
	if err := os.Mkdir(filepath.Join(workDir, "late"), 0o755); err != nil {
		t.Fatal(err)
	}

	select {
	case <-subscription.done:
	case <-time.After(2 * time.Second):
		t.Fatal("resource stream stayed healthy after a directory watch failed")
	}
}

func TestResourceEventHubResynchronizesAfterWatcherError(t *testing.T) {
	hub := newResourceEventHub(t.TempDir(), t.TempDir())
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	hub.resyncAfterWatcherError()
	select {
	case <-subscription.updates:
		got := subscription.take().Resources
		want := []string{resourceObservable, resourceRuntime, resourceScratchpad, resourceWorkspace}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("resources = %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("watcher error did not invalidate all resources")
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

func TestResourceEventHubCloseEndsActiveSubscriptions(t *testing.T) {
	hub := newResourceEventHub(t.TempDir(), t.TempDir())
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}

	hub.close()
	select {
	case <-subscription.done:
	case <-time.After(time.Second):
		t.Fatal("resource subscription stayed open after hub close")
	}
	// Cancellation remains idempotent after global shutdown.
	subscription.cancel()
	if _, err := hub.subscribe(); err == nil {
		t.Fatal("closed resource hub accepted a new subscription")
	}
}

func TestResourceEventsEndpointEndsWhenServerCloses(t *testing.T) {
	server := NewServer(Options{})
	httpServer := httptest.NewServer(server.APIHandler())
	defer httpServer.Close()

	response, err := http.Get(httpServer.URL + "/api/resource-events")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}

	server.Close()
	done := make(chan error, 1)
	go func() {
		_, err := reader.ReadString('\n')
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("resource stream returned another frame after server close")
		}
	case <-time.After(time.Second):
		t.Fatal("resource stream did not end after server close")
	}
}
