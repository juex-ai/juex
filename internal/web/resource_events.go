package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/juex-ai/juex/internal/events"
	"github.com/juex-ai/juex/internal/toolevents"
)

const resourceChangeDebounce = 120 * time.Millisecond

const (
	resourceWorkspace  = "workspace"
	resourceScratchpad = "scratchpad"
	resourceObservable = "observables"
	resourceRuntime    = "runtime"
)

type agentResourceEvent struct {
	Type      string   `json:"type"`
	Resources []string `json:"resources"`
}

type resourceSubscriber struct {
	mu      sync.Mutex
	pending map[string]struct{}
	notify  chan struct{}
}

func newResourceSubscriber() *resourceSubscriber {
	return &resourceSubscriber{
		pending: map[string]struct{}{},
		notify:  make(chan struct{}, 1),
	}
}

func (s *resourceSubscriber) publish(resources ...string) {
	s.mu.Lock()
	for _, resource := range resources {
		if resource != "" {
			s.pending[resource] = struct{}{}
		}
	}
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *resourceSubscriber) take() agentResourceEvent {
	s.mu.Lock()
	resources := make([]string, 0, len(s.pending))
	for resource := range s.pending {
		resources = append(resources, resource)
	}
	s.pending = map[string]struct{}{}
	s.mu.Unlock()
	sort.Strings(resources)
	return agentResourceEvent{Type: "resource.changed", Resources: resources}
}

type resourceEventHub struct {
	workDir     string
	sessionsDir string

	mu            sync.Mutex
	subscribers   map[uint64]*resourceSubscriber
	nextID        uint64
	watcher       *fsnotify.Watcher
	cancel        context.CancelFunc
	watcherReady  chan struct{}
	invalidations *resourceSubscriber
}

type resourceSubscription struct {
	updates <-chan struct{}
	take    func() agentResourceEvent
	cancel  func()
}

func newResourceEventHub(workDir, sessionsDir string) *resourceEventHub {
	return &resourceEventHub{
		workDir:     filepath.Clean(workDir),
		sessionsDir: filepath.Clean(sessionsDir),
		subscribers: map[uint64]*resourceSubscriber{},
	}
}

func (h *resourceEventHub) subscribe() (resourceSubscription, error) {
	var startWatcher bool
	var watcherReady chan struct{}
	h.mu.Lock()
	if h.watcher == nil {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			h.mu.Unlock()
			return resourceSubscription{}, err
		}
		ctx, cancel := context.WithCancel(context.Background())
		h.watcher = watcher
		h.cancel = cancel
		h.watcherReady = make(chan struct{})
		h.invalidations = newResourceSubscriber()
		startWatcher = true
		watcherReady = h.watcherReady
		go h.runWatcher(ctx, watcher, h.invalidations)
	} else {
		watcherReady = h.watcherReady
	}
	h.nextID++
	id := h.nextID
	subscriber := newResourceSubscriber()
	h.subscribers[id] = subscriber
	watcher := h.watcher
	h.mu.Unlock()
	if startWatcher {
		h.addRoot(watcher, h.workDir)
		h.addRoot(watcher, h.sessionsDir)
		_ = watcher.Add(filepath.Join(h.workDir, ".juex"))
		close(watcherReady)
	} else if watcherReady != nil {
		<-watcherReady
	}

	return resourceSubscription{
		updates: subscriber.notify,
		take:    subscriber.take,
		cancel: func() {
			var cancel context.CancelFunc
			var watcher *fsnotify.Watcher
			h.mu.Lock()
			delete(h.subscribers, id)
			if len(h.subscribers) == 0 {
				cancel = h.cancel
				watcher = h.watcher
				h.cancel = nil
				h.watcher = nil
				h.watcherReady = nil
				h.invalidations = nil
			}
			h.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			if watcher != nil {
				_ = watcher.Close()
			}
		},
	}, nil
}

func (h *resourceEventHub) addRoot(watcher *fsnotify.Watcher, root string) {
	if root == "" || root == "." {
		return
	}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root && skipWatchedDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		_ = watcher.Add(path)
		return nil
	})
}

func skipWatchedDirectory(name string) bool {
	switch name {
	case ".git", ".juex", "node_modules", "dist":
		return true
	default:
		return false
	}
}

func (h *resourceEventHub) runWatcher(
	ctx context.Context,
	watcher *fsnotify.Watcher,
	invalidations *resourceSubscriber,
) {
	pending := map[string]struct{}{}
	var timer *time.Timer
	var timerC <-chan time.Time
	queue := func(resources ...string) {
		for _, resource := range resources {
			if resource != "" {
				pending[resource] = struct{}{}
			}
		}
		if len(pending) == 0 {
			return
		}
		if timer == nil {
			timer = time.NewTimer(resourceChangeDebounce)
		} else if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(resourceChangeDebounce)
		timerC = timer.C
	}
	flush := func() {
		resources := make([]string, 0, len(pending))
		for resource := range pending {
			resources = append(resources, resource)
		}
		pending = map[string]struct{}{}
		h.publish(resources...)
	}
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()
	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			resource := h.resourceForPath(event.Name)
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					if filepath.Clean(event.Name) == filepath.Join(h.workDir, ".juex") {
						_ = watcher.Add(event.Name)
						resource = resourceObservable
					} else {
						h.addRoot(watcher, event.Name)
					}
				}
			}
			if resource != "" {
				queue(resource)
			}
		case <-invalidations.notify:
			queue(invalidations.take().Resources...)
		case <-timerC:
			flush()
			timerC = nil
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
			// A single watch failure must not terminate the shared SSE stream.
		case <-ctx.Done():
			return
		}
	}
}

func (h *resourceEventHub) resourceForPath(path string) string {
	path = filepath.Clean(path)
	if pathWithin(h.sessionsDir, path) && strings.Contains(path, string(filepath.Separator)+"scratchpad") {
		return resourceScratchpad
	}
	if !pathWithin(h.workDir, path) {
		return ""
	}
	relative, err := filepath.Rel(h.workDir, path)
	if err != nil || relative == "." {
		return ""
	}
	parts := strings.Split(relative, string(filepath.Separator))
	if len(parts) > 0 && shouldSkipTreeEntry(parts[0]) {
		if filepath.Clean(path) == filepath.Clean(filepath.Join(h.workDir, ".juex", "observables.json")) {
			return resourceObservable
		}
		return ""
	}
	return resourceWorkspace
}

func pathWithin(root, path string) bool {
	if root == "" || root == "." || path == "" || path == "." {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func (h *resourceEventHub) publish(resources ...string) {
	if h == nil || len(resources) == 0 {
		return
	}
	h.mu.Lock()
	subscribers := make([]*resourceSubscriber, 0, len(h.subscribers))
	for _, subscriber := range h.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	h.mu.Unlock()
	for _, subscriber := range subscribers {
		subscriber.publish(resources...)
	}
}

func (h *resourceEventHub) Publish(event events.Event) {
	switch {
	case strings.HasPrefix(event.Type, "observable."), strings.HasPrefix(event.Type, "observation."):
		h.invalidate(resourceObservable)
	case event.Type == toolevents.CompletedType || event.Type == toolevents.ErroredType:
		name := toolEventName(event.Payload)
		if name == "write" || name == "edit" || name == "apply_patch" || name == "write_commit" {
			h.invalidate(resourceWorkspace, resourceScratchpad)
		}
	}
}

func (h *resourceEventHub) invalidate(resources ...string) {
	if h == nil || len(resources) == 0 {
		return
	}
	h.mu.Lock()
	invalidations := h.invalidations
	h.mu.Unlock()
	if invalidations == nil {
		return
	}
	invalidations.publish(resources...)
}

func toolEventName(payload any) string {
	switch typed := payload.(type) {
	case toolevents.CompletedPayload:
		return typed.Name
	case *toolevents.CompletedPayload:
		return typed.Name
	case toolevents.ErroredPayload:
		return typed.Name
	case *toolevents.ErroredPayload:
		return typed.Name
	default:
		return ""
	}
}

func (h *resourceEventHub) close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	cancel := h.cancel
	watcher := h.watcher
	h.cancel = nil
	h.watcher = nil
	h.watcherReady = nil
	h.invalidations = nil
	h.subscribers = map[uint64]*resourceSubscriber{}
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if watcher != nil {
		_ = watcher.Close()
	}
}

func (s *Server) handleResourceEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET required")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "general_error", "streaming not supported")
		return
	}
	subscription, err := s.resources.subscribe()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "general_error", "resource stream unavailable")
		return
	}
	defer subscription.cancel()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	if err := writeResourceSSE(w, agentResourceEvent{
		Type:      "resource.changed",
		Resources: []string{resourceObservable, resourceRuntime, resourceScratchpad, resourceWorkspace},
	}); err != nil {
		return
	}
	flusher.Flush()
	for {
		select {
		case <-subscription.updates:
			event := subscription.take()
			if len(event.Resources) == 0 {
				continue
			}
			if err := writeResourceSSE(w, event); err != nil {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func writeResourceSSE(w http.ResponseWriter, event agentResourceEvent) error {
	body, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "data: %s\n\n", body); err != nil {
		return err
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

var _ events.Delivery = (*resourceEventHub)(nil)
