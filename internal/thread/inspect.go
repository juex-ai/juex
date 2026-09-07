package thread

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Inspection is validated metadata only. Inspect never opens the EventStore or
// repairs projections, generation files, indexes, or Context renewal staging.
type Inspection struct {
	Dir        string
	Projection Projection
}

func (s *Store) Inspect(id string) (Inspection, error) {
	if !ValidID(id) {
		return Inspection{}, fmt.Errorf("%w: %q", ErrInvalidID, id)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, root := range []string{s.ThreadsDir(), s.ArchiveDir()} {
		dir := filepath.Join(root, id)
		if err := validateInspectionDirectory(s.agentStateDir, dir); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return Inspection{}, err
		}
		metadataInfo, err := os.Lstat(filepath.Join(dir, projectionFile))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Inspection{}, err
		}
		if !metadataInfo.Mode().IsRegular() {
			return Inspection{}, fmt.Errorf("%w: metadata is not a regular file", ErrInvalidMetadata)
		}
		projection, err := readProjectionFile(dir, id)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Inspection{}, err
		}
		if (projection.RetentionState == RetentionArchived) != (root == s.ArchiveDir()) {
			return Inspection{}, fmt.Errorf("%w for %s: retention namespace mismatch", ErrInvalidMetadata, id)
		}
		return Inspection{Dir: dir, Projection: projection}, nil
	}
	return Inspection{}, os.ErrNotExist
}

func validateInspectionDirectory(root, dir string) error {
	relative, err := filepath.Rel(root, dir)
	if err != nil {
		return err
	}
	current := root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%w: Thread namespace is not a directory", ErrInvalidMetadata)
		}
	}
	return nil
}
