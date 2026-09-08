package thread

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ResourceDirectories enumerates stored Thread scopes without opening metadata,
// recovering journals, or creating an index. Callers exclude active writers.
func (s *Store) ResourceDirectories() ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var dirs []string
	for _, root := range []string{s.ThreadsDir(), s.ArchiveDir()} {
		for path := root; path != s.agentStateDir; path = filepath.Dir(path) {
			info, err := os.Lstat(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("thread: invalid resource root %s", path)
			}
		}
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if !ValidID(entry.Name()) {
				continue
			}
			if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("thread: invalid resource scope %s", filepath.Join(root, entry.Name()))
			}
			dirs = append(dirs, filepath.Join(root, entry.Name()))
		}
	}
	return dirs, nil
}
