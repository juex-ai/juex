package state

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/foundation/homestore"
)

func RemoveFile(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return syncParentDirectory(path)
}

func syncParentDirectory(path string) error { return homestore.SyncDir(filepath.Dir(path)) }
