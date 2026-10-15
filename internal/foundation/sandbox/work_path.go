package sandbox

import (
	"path/filepath"
)

func ResolveWorkPath(workDir, path string) string {
	if path == "" || filepath.IsAbs(path) || workDir == "" {
		return path
	}
	return filepath.Join(workDir, path)
}
