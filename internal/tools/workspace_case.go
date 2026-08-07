package tools

import (
	"os"
	"path/filepath"
)

func workspaceCaseInsensitive(root string) bool {
	entries, err := os.ReadDir(root)
	if err == nil {
		for _, entry := range entries {
			alternateName, ok := toggleASCIIPathCase(entry.Name())
			if !ok {
				continue
			}
			original, originalErr := os.Lstat(filepath.Join(root, entry.Name()))
			if originalErr != nil {
				continue
			}
			alternate, alternateErr := os.Lstat(filepath.Join(root, alternateName))
			if alternateErr != nil {
				return false
			}
			return os.SameFile(original, alternate)
		}
	}
	return probeEmptyWorkspaceCase(root)
}

func probeEmptyWorkspaceCase(root string) bool {
	probe, err := os.CreateTemp(root, ".juex-case-probe-")
	if err != nil {
		return false
	}
	probePath := probe.Name()
	probeInfo, statErr := probe.Stat()
	closeErr := probe.Close()
	alternateName, ok := toggleASCIIPathCase(filepath.Base(probePath))
	if statErr != nil || closeErr != nil || !ok {
		_ = os.Remove(probePath)
		return false
	}
	alternateInfo, alternateErr := os.Stat(filepath.Join(root, alternateName))
	removeErr := os.Remove(probePath)
	if alternateErr != nil || removeErr != nil {
		return false
	}
	return os.SameFile(probeInfo, alternateInfo)
}

func toggleASCIIPathCase(name string) (string, bool) {
	bytes := []byte(name)
	for i, char := range bytes {
		switch {
		case char >= 'a' && char <= 'z':
			bytes[i] = char - ('a' - 'A')
			return string(bytes), true
		case char >= 'A' && char <= 'Z':
			bytes[i] = char + ('a' - 'A')
			return string(bytes), true
		}
	}
	return name, false
}
