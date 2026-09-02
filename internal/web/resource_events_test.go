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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/observable"
	"github.com/juex-ai/juex/internal/thread"
	"github.com/juex-ai/juex/internal/toolevents"
)

func TestResourceEventHubClassifiesWorkspaceAndRuntimePaths(t *testing.T) {
	workDir := t.TempDir()
	threadsDir := filepath.Join(t.TempDir(), "threads")
	hub := newResourceEventHub(workDir, threadsDir)

	tests := []struct {
		path string
		want string
	}{
		{path: filepath.Join(workDir, "src", "main.go"), want: resourceWorkspace},
		{path: filepath.Join(workDir, ".git", "index")},
		{path: filepath.Join(workDir, ".juex", "events.jsonl")},
		{path: filepath.Join(workDir, ".juex", "observables.json"), want: resourceObservable},
		{path: filepath.Join(threadsDir, "123456", "scratchpad", "notes.md"), want: resourceScratchpad},
	}
	for _, test := range tests {
		if got := hub.resourceForPath(test.path); got != test.want {
			t.Errorf("resourceForPath(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestResourceEventHubClassifiesScratchpadAsRuntimeInput(t *testing.T) {
	threadsDir := filepath.Join(t.TempDir(), "threads")
	hub := newResourceEventHub(t.TempDir(), threadsDir)
	path := filepath.Join(threadsDir, "123456", "scratchpad", "notes.md")

	got := hub.resourcesForPath(path)
	want := []string{resourceScratchpad, resourceRuntime}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resourcesForPath(%q) = %v, want %v", path, got, want)
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
	historyPath := filepath.Join(t.TempDir(), "history.json")
	hub := newResourceEventHub(workDir, t.TempDir())
	hub.setRuntimeInputs([]string{filepath.Join(globalAgentsDir, "AGENTS.md"), historyPath})

	tests := []struct {
		path string
		want []string
	}{
		{path: globalAgentsDir, want: []string{resourceRuntime}},
		{path: filepath.Join(globalAgentsDir, "AGENTS.md"), want: []string{resourceRuntime}},
		{path: historyPath, want: []string{resourceRuntime}},
		{path: filepath.Join(globalAgentsDir, "skills", "demo", "SKILL.md")},
	}
	for _, test := range tests {
		if got := hub.resourcesForPath(test.path); !reflect.DeepEqual(got, test.want) {
			t.Errorf("resourcesForPath(%q) = %v, want %v", test.path, got, test.want)
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
	want := []string{resourceObservable, resourceRuntime, resourceScratchpad, resourceWorkspace}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("resources = %v, want %v", got, want)
	}
}

func TestResourceEventHubCoalescesProjectedAndFilesystemChanges(t *testing.T) {
	workDir := t.TempDir()
	threadsDir := filepath.Join(t.TempDir(), "threads")
	if err := os.MkdirAll(threadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hub := newResourceEventHub(workDir, threadsDir)
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
		want := []string{resourceRuntime, resourceScratchpad, resourceWorkspace}
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
	threadsDir := filepath.Join(t.TempDir(), "threads")
	if err := os.MkdirAll(threadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hub := newResourceEventHub(workDir, threadsDir)
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
	hub.setRuntimeInputs([]string{filepath.Join(globalAgentsDir, "AGENTS.md")})
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
		got := subscription.take().Resources
		if !slices.Contains(got, resourceRuntime) {
			t.Fatalf("resources = %v, want runtime invalidation", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("external global AGENTS.md mutation was not observed")
	}
}

func TestResourceEventHubReanchorsRecreatedExternalRuntimeDirectory(t *testing.T) {
	existingRoot := t.TempDir()
	globalAgentsDir := filepath.Join(existingRoot, ".agents")
	if err := os.Mkdir(globalAgentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hub := newResourceEventHub(t.TempDir(), t.TempDir())
	hub.setRuntimeInputs([]string{filepath.Join(globalAgentsDir, "AGENTS.md")})
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	if err := os.Rename(globalAgentsDir, globalAgentsDir+".old"); err != nil {
		t.Fatal(err)
	}
	assertRuntimeInvalidation(t, subscription, "renamed external global resource directory")
	if err := os.Mkdir(globalAgentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	assertRuntimeInvalidation(t, subscription, "recreated external global resource directory")
	if err := os.WriteFile(filepath.Join(globalAgentsDir, "AGENTS.md"), []byte("replacement guidance"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRuntimeInvalidation(t, subscription, "recreated external global AGENTS.md mutation")
}

func TestResourceEventHubWatchesLateExternalGlobalAgentsDirectory(t *testing.T) {
	existingRoot := t.TempDir()
	globalAgentsDir := filepath.Join(existingRoot, "missing", ".agents")
	hub := newResourceEventHub(t.TempDir(), t.TempDir())
	hub.setRuntimeInputs([]string{filepath.Join(globalAgentsDir, "AGENTS.md")})
	subscription, err := hub.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	if err := os.Mkdir(filepath.Join(existingRoot, "missing"), 0o755); err != nil {
		t.Fatal(err)
	}
	assertRuntimeInvalidation(t, subscription, "late external global resource ancestor")
	if err := os.Mkdir(globalAgentsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	assertRuntimeInvalidation(t, subscription, "late external global resource directory")
	if err := os.WriteFile(filepath.Join(globalAgentsDir, "AGENTS.md"), []byte("global guidance"), 0o644); err != nil {
		t.Fatal(err)
	}
	assertRuntimeInvalidation(t, subscription, "late external global AGENTS.md mutation")
}

func assertRuntimeInvalidation(t *testing.T, subscription resourceSubscription, change string) {
	t.Helper()
	select {
	case <-subscription.updates:
		if got := subscription.take().Resources; !reflect.DeepEqual(got, []string{resourceRuntime}) {
			t.Fatalf("resources = %v, want runtime", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%s was not observed", change)
	}
}

func TestResourceEventHubWatchesExternalThreadIndexChange(t *testing.T) {
	srv := newTestServer(t)

	subscription, err := srv.resources.subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.cancel()

	created, err := thread.NewStore(srv.opts.Cfg.RuntimePaths().StateDir).CreateWorker(thread.MainID, "external-index-change")
	if err != nil {
		t.Fatal(err)
	}
	_ = created.Close()
	select {
	case <-subscription.updates:
		got := subscription.take().Resources
		if !slices.Contains(got, resourceRuntime) {
			t.Fatalf("resources = %v, want runtime invalidation", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("external Thread index change did not invalidate runtime")
	}
}

func TestResourceEventHubWatchesLateObservableConfigWithoutRuntimeTree(t *testing.T) {
	workDir := t.TempDir()
	threadsDir := filepath.Join(t.TempDir(), "threads")
	if err := os.MkdirAll(threadsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hub := newResourceEventHub(workDir, threadsDir)
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
