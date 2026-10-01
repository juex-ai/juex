package blob

import (
	"io"
	"os"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// OpenReader exposes a read-only handle to an immutable object, not its path.
// The caller owns authority and must close the handle after bounded streaming.
func (s *Store) OpenReader(id string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := s.object(id)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	status, err := readStatus(root)
	if err != nil {
		return nil, err
	}
	if !status.Ready {
		return nil, execprotocol.ErrConflict
	}
	file, _, err := dataFile(root, status, os.O_RDONLY)
	return file, err
}
