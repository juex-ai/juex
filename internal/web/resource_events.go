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
	mu        sync.Mutex
	pending   map[string]struct{}
	notify    chan struct{}
	done      chan struct{}
	closeOnce sync.Once
}

func newResourceSubscriber() *resourceSubscriber {
	return &resourceSubscriber{
		pending: map[string]struct{}{},
		notify:  make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
}

func (s *resourceSubscriber) close() {
	s.closeOnce.Do(func() { close(s.done) })
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
	workDir      string
	threadsDir   string
	runtimeFiles map[string]struct{}

	mu            sync.Mutex
	subscribers   map[uint64]*resourceSubscriber
	nextID        uint64
	watcher       *fsnotify.Watcher
	cancel        context.CancelFunc
	invalidations *resourceSubscriber
	addWatch      func(*fsnotify.Watcher, string) error
	closed        bool
}

type resourceSubscription struct {
	updates <-chan struct{}
	done    <-chan struct{}
	take    func() agentResourceEvent
	cancel  func()
}

func newResourceEventHub(workDir, threadsDir string) *resourceEventHub {
	return &resourceEventHub{
		workDir:      filepath.Clean(workDir),
		threadsDir:   filepath.Clean(threadsDir),
		runtimeFiles: map[string]struct{}{},
		subscribers:  map[uint64]*resourceSubscriber{},
		addWatch: func(watcher *fsnotify.Watcher, path string) error {
			return watcher.Add(path)
		},
	}
}

func (h *resourceEventHub) setRuntimeInputs(files []string) {
	for _, file := range files {
		file = filepath.Clean(file)
		if file != "" && file != "." {
			h.runtimeFiles[file] = struct{}{}
		}
	}
}

func (h *resourceEventHub) subscribe() (resourceSubscription, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return resourceSubscription{}, fmt.Errorf("resource event hub is closed")
	}
	if h.watcher == nil {
		watcher, err := fsnotify.NewWatcher()
		if err != nil {
			h.mu.Unlock()
			return resourceSubscription{}, err
		}
		if err := h.addRoot(watcher, h.workDir); err != nil {
			_ = watcher.Close()
			h.mu.Unlock()
			return resourceSubscription{}, err
		}
		if err := h.addRoot(watcher, h.threadsDir); err != nil {
			_ = watcher.Close()
			h.mu.Unlock()
			return resourceSubscription{}, err
		}
		watchedRuntimeParents := map[string]struct{}{}
		for file := range h.runtimeFiles {
			parent := filepath.Dir(file)
			if _, exists := watchedRuntimeParents[parent]; exists {
				continue
			}
			if err := h.addReplaceableRuntimeDirectory(watcher, parent); err != nil {
				_ = watcher.Close()
				h.mu.Unlock()
				return resourceSubscription{}, err
			}
			watchedRuntimeParents[parent] = struct{}{}
		}
		observableDir := filepath.Join(h.workDir, ".juex")
		if info, statErr := os.Stat(observableDir); statErr == nil && info.IsDir() {
			if err := h.addWatch(watcher, observableDir); err != nil {
				_ = watcher.Close()
				h.mu.Unlock()
				return resourceSubscription{}, fmt.Errorf("watch observable config directory %q: %w", observableDir, err)
			}
		} else if statErr != nil && !os.IsNotExist(statErr) {
			_ = watcher.Close()
			h.mu.Unlock()
			return resourceSubscription{}, fmt.Errorf("inspect observable config directory %q: %w", observableDir, statErr)
		}
		ctx, cancel := context.WithCancel(context.Background())
		h.watcher = watcher
		h.cancel = cancel
		h.invalidations = newResourceSubscriber()
		go h.runWatcher(ctx, watcher, h.invalidations)
	}
	h.nextID++
	id := h.nextID
	subscriber := newResourceSubscriber()
	h.subscribers[id] = subscriber
	h.mu.Unlock()

	return resourceSubscription{
		updates: subscriber.notify,
		done:    subscriber.done,
		take:    subscriber.take,
		cancel: func() {
			var cancel context.CancelFunc
			h.mu.Lock()
			_, subscribed := h.subscribers[id]
			if subscribed {
				delete(h.subscribers, id)
			}
			if subscribed && len(h.subscribers) == 0 {
				cancel = h.cancel
				h.cancel = nil
				h.watcher = nil
				h.invalidations = nil
			}
			h.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			subscriber.close()
		},
	}, nil
}

func (h *resourceEventHub) addReplaceableRuntimeDirectory(watcher *fsnotify.Watcher, target string) error {
	target = filepath.Clean(target)
	watched, err := h.addNearestExistingDirectory(watcher, target)
	if err != nil || watched != target {
		return err
	}
	parent := filepath.Dir(target)
	if parent == target {
		return nil
	}
	_, err = h.addNearestExistingDirectory(watcher, parent)
	return err
}

func (h *resourceEventHub) addNearestExistingDirectory(watcher *fsnotify.Watcher, target string) (string, error) {
	for dir := filepath.Clean(target); ; dir = filepath.Dir(dir) {
		info, err := os.Stat(dir)
		if err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("runtime input ancestor %q is not a directory", dir)
			}
			if err := h.addWatch(watcher, dir); err != nil {
				return "", fmt.Errorf("watch runtime input ancestor %q: %w", dir, err)
			}
			return dir, nil
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect runtime input ancestor %q: %w", dir, err)
		}
		if parent := filepath.Dir(dir); parent == dir {
			return "", fmt.Errorf("no existing directory ancestor for runtime input %q", target)
		}
	}
}

func (h *resourceEventHub) addRoot(watcher *fsnotify.Watcher, root string) error {
	if root == "" || root == "." {
		return nil
	}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			return nil
		}
		if path != root && skipWatchedDirectory(entry.Name()) {
			return filepath.SkipDir
		}
		if err := h.addWatch(watcher, path); err != nil {
			return fmt.Errorf("watch directory %q: %w", path, err)
		}
		return nil
	})
	if os.IsNotExist(err) {
		return nil
	}
	return err
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
	defer func() { _ = watcher.Close() }()
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
			resources := h.resourcesForPath(event.Name)
			if event.Op&fsnotify.Create != 0 {
				if info, err := os.Stat(event.Name); err == nil && info.IsDir() {
					if filepath.Clean(event.Name) == filepath.Join(h.workDir, ".juex") {
						if err := h.addWatch(watcher, event.Name); err != nil {
							h.failWatcher(watcher)
							return
						}
						resources = []string{resourceObservable}
					} else if h.shouldWatchCreatedDirectory(event.Name) {
						if err := h.addRoot(watcher, event.Name); err != nil {
							h.failWatcher(watcher)
							return
						}
					}
				}
			}
			queue(resources...)
		case <-invalidations.notify:
			queue(invalidations.take().Resources...)
		case <-timerC:
			flush()
			timerC = nil
		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}
			h.resyncAfterWatcherError()
		case <-ctx.Done():
			return
		}
	}
}

func (h *resourceEventHub) shouldWatchCreatedDirectory(path string) bool {
	path = filepath.Clean(path)
	if pathWithin(h.workDir, path) || pathWithin(h.threadsDir, path) {
		return true
	}
	for file := range h.runtimeFiles {
		if pathWithin(path, filepath.Dir(file)) {
			return true
		}
	}
	return false
}

func (h *resourceEventHub) resyncAfterWatcherError() {
	h.publish(resourceObservable, resourceRuntime, resourceScratchpad, resourceWorkspace)
}

func (h *resourceEventHub) failWatcher(failed *fsnotify.Watcher) {
	h.mu.Lock()
	if h.watcher != failed {
		h.mu.Unlock()
		return
	}
	cancel := h.cancel
	h.cancel = nil
	h.watcher = nil
	h.invalidations = nil
	subscribers := make([]*resourceSubscriber, 0, len(h.subscribers))
	for _, subscriber := range h.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	h.subscribers = map[uint64]*resourceSubscriber{}
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, subscriber := range subscribers {
		subscriber.close()
	}
}

func (h *resourceEventHub) resourceForPath(path string) string {
	path = filepath.Clean(path)
	if pathWithin(h.threadsDir, path) && strings.Contains(path, string(filepath.Separator)+"scratchpad") {
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

func (h *resourceEventHub) resourcesForPath(path string) []string {
	resource := h.resourceForPath(path)
	if resource == "" {
		if h.isMutableRuntimeInput(path) {
			return []string{resourceRuntime}
		}
		return nil
	}
	resources := []string{resource}
	if resource == resourceScratchpad || (resource == resourceWorkspace && h.isMutableRuntimeInput(path)) {
		resources = append(resources, resourceRuntime)
	}
	return resources
}

func (h *resourceEventHub) isMutableRuntimeInput(path string) bool {
	path = filepath.Clean(path)
	if _, ok := h.runtimeFiles[path]; ok {
		return true
	}
	for file := range h.runtimeFiles {
		if path == filepath.Dir(file) || pathWithin(path, filepath.Dir(file)) {
			return true
		}
	}
	relative, err := filepath.Rel(h.workDir, path)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false
	}
	return relative == "AGENTS.md" ||
		relative == ".agents" ||
		relative == filepath.Join(".agents", "AGENTS.md")
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
		switch name {
		case "write", "edit", "apply_patch", "write_commit":
			h.invalidate(resourceWorkspace, resourceScratchpad, resourceRuntime)
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
	h.closed = true
	cancel := h.cancel
	h.cancel = nil
	h.watcher = nil
	h.invalidations = nil
	subscribers := make([]*resourceSubscriber, 0, len(h.subscribers))
	for _, subscriber := range h.subscribers {
		subscribers = append(subscribers, subscriber)
	}
	h.subscribers = map[uint64]*resourceSubscriber{}
	h.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, subscriber := range subscribers {
		subscriber.close()
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
		case <-subscription.done:
			return
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
