package thread

import "fmt"

// Depth follows authoritative metadata in both retention namespaces, without
// opening journals or repairing the list index. Main has depth zero.
func (s *Store) Depth(id string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.depthLocked(id)
}

func (s *Store) depthLocked(id string) (int, error) {
	seen := make(map[string]bool)
	for depth := 0; ; depth++ {
		if seen[id] {
			return 0, fmt.Errorf("%w: parent cycle includes Thread %s", ErrInvalidMetadata, id)
		}
		seen[id] = true
		metadata, err := s.inspectLocked(id)
		if err != nil {
			return 0, fmt.Errorf("thread: resolve parent chain at %s: %w", id, err)
		}
		if id == MainID {
			return depth, nil
		}
		id = metadata.Projection.ParentThreadID
	}
}

// CheckWorkerDepth lets execution entrypoints reject before startup side
// effects. CreateWorker repeats the same check under the creation lock.
func (s *Store) CheckWorkerDepth(parentID string, maxDepth int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkWorkerDepthLocked(parentID, maxDepth)
}

func (s *Store) checkWorkerDepthLocked(parentID string, maxDepth int) error {
	if maxDepth < 1 {
		return fmt.Errorf("thread: max_depth must be positive")
	}
	depth, err := s.depthLocked(parentID)
	if err != nil {
		return err
	}
	if depth >= maxDepth {
		return fmt.Errorf("thread: parent %s has depth %d; worker creation exceeds max_depth=%d", parentID, depth, maxDepth)
	}
	return nil
}
