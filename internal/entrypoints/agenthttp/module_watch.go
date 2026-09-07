package web

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// moduleWatcher observes only declared state paths and their existing ancestor
// directories. It never walks a module body or creates a missing directory.
type moduleWatcher struct {
	resourceRoot string
	watcher      *fsnotify.Watcher
	root         string
	paths        []string
	changed      chan struct{}
	done         chan struct{}
	once         sync.Once
}

func newModuleWatcher(root string, paths []string) (*moduleWatcher, error) {
	return newScopedModuleWatcher(root, paths, "")
}

func newScopedModuleWatcher(root string, paths []string, resourceRoot string) (*moduleWatcher, error) {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &moduleWatcher{resourceRoot: resourceRoot, watcher: watcher, root: filepath.Clean(root), paths: paths, changed: make(chan struct{}, 1), done: make(chan struct{})}
	if err := w.Refresh(); err != nil {
		_ = watcher.Close()
		return nil, err
	}
	go func() {
		defer close(w.done)
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if w.relevant(event.Name) {
					w.invalidate()
				}
			case _, ok := <-watcher.Errors:
				if !ok {
					return
				}
				w.invalidate()
			}
		}
	}()
	return w, nil
}
func (w *moduleWatcher) relevant(path string) bool {
	if w.resourceRoot != "" && pathWithin(w.resourceRoot, path) {
		return true
	}
	for _, target := range w.paths {
		if path == target || pathWithin(path, filepath.Dir(target)) {
			return true
		}
	}
	return false
}
func (w *moduleWatcher) invalidate() {
	select {
	case w.changed <- struct{}{}:
	default:
	}
}
func (w *moduleWatcher) Changed() <-chan struct{} { return w.changed }
func (w *moduleWatcher) Done() <-chan struct{}    { return w.done }
func (w *moduleWatcher) Close()                   { w.once.Do(func() { _ = w.watcher.Close(); <-w.done }) }
func (w *moduleWatcher) Refresh() error {
	watched := map[string]bool{}
	for _, path := range w.watcher.WatchList() {
		watched[path] = true
	}
	for _, target := range w.paths {
		relative, err := relativeInside(w.root, filepath.Dir(target))
		if err != nil {
			return err
		}
		if err := rejectModuleTreeSymlinks(w.root, relative); err != nil {
			return err
		}
		if err := watchModuleParents(w.root, filepath.Dir(target), watched, w.watcher.Add); err != nil {
			return err
		}
	}
	if w.resourceRoot != "" {
		err := filepath.WalkDir(w.resourceRoot, func(path string, entry os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return nil
			}
			if err != nil {
				return err
			}
			if !entry.IsDir() {
				return nil
			}
			if !watched[path] {
				if err := w.watcher.Add(path); err != nil && !os.IsNotExist(err) {
					return err
				}
				watched[path] = true
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func watchModuleParents(root, directory string, watched map[string]bool, add func(string) error) error {
	var directories []string
	for dir := directory; pathWithin(root, dir); dir = filepath.Dir(dir) {
		directories = append(directories, dir)
		if dir == root {
			break
		}
	}
	// Subscribe to parents before checking descendants. A child created while
	// its parent is being added must either be discovered next or emit an event.
	for i := len(directories) - 1; i >= 0; i-- {
		dir := directories[i]
		if !watched[dir] {
			info, err := os.Stat(dir)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err := add(dir); err != nil {
					if os.IsNotExist(err) {
						continue
					}
					return err
				}
				watched[dir] = true
			}
		}
	}
	return nil
}
