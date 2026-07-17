package agentstate

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
)

var legacyStateEntries = []string{"sessions", "memory", "history.json", "logs"}

var verifyCopiedTree = compareTree

func publishNewAgent(homeDir, workDir string, agent Agent) (agentDir string, migrated bool, err error) {
	agentsDir := filepath.Join(homeDir, "agents")
	agentDir = filepath.Join(agentsDir, agent.ID)
	stageDir, err := os.MkdirTemp(agentsDir, "."+agent.ID+".creating-")
	if err != nil {
		return "", false, fmt.Errorf("agentstate: create staging directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(stageDir)
		}
	}()

	for _, name := range []string{"sessions", "memory", "logs"} {
		if err := os.MkdirAll(filepath.Join(stageDir, name), 0o755); err != nil {
			return "", false, err
		}
	}
	legacyDir := filepath.Join(workDir, ".juex")
	for _, name := range legacyStateEntries {
		source := filepath.Join(legacyDir, name)
		if _, statErr := os.Lstat(source); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return "", false, fmt.Errorf("agentstate: inspect legacy state %s: %w", source, statErr)
		}
		migrated = true
		destination := filepath.Join(stageDir, name)
		if err := os.RemoveAll(destination); err != nil {
			return "", false, err
		}
		if err := copyTree(source, destination); err != nil {
			return "", false, fmt.Errorf("agentstate: copy legacy state %s: %w", source, err)
		}
		if err := verifyCopiedTree(source, destination); err != nil {
			return "", false, fmt.Errorf("agentstate: verify legacy state %s: %w", source, err)
		}
	}
	for _, name := range []string{"sessions", "memory", "logs"} {
		if err := os.MkdirAll(filepath.Join(stageDir, name), 0o755); err != nil {
			return "", false, err
		}
	}
	historyPath := filepath.Join(stageDir, "history.json")
	if _, statErr := os.Stat(historyPath); errors.Is(statErr, os.ErrNotExist) {
		if err := atomicWriteFile(historyPath, []byte("{\"sessions\":[]}\n"), 0o644); err != nil {
			return "", false, err
		}
	} else if statErr != nil {
		return "", false, statErr
	}
	if err := atomicWriteJSON(filepath.Join(stageDir, agentFileName), agent, 0o644); err != nil {
		return "", false, err
	}
	if err := syncDir(stageDir); err != nil {
		return "", false, fmt.Errorf("agentstate: sync staging directory: %w", err)
	}
	if err := os.Rename(stageDir, agentDir); err != nil {
		return "", false, fmt.Errorf("agentstate: publish agent directory %s: %w", agentDir, err)
	}
	if err := syncDir(agentsDir); err != nil {
		_ = os.RemoveAll(agentDir)
		_ = syncDir(agentsDir)
		return "", false, fmt.Errorf("agentstate: sync agent registry: %w", err)
	}
	return agentDir, migrated, nil
}

func cleanupPublishedLegacyState(workDir, agentDir string) (bool, error) {
	legacyDir := filepath.Join(workDir, ".juex")
	found := false
	for _, name := range legacyStateEntries {
		source := filepath.Join(legacyDir, name)
		if _, err := os.Lstat(source); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, fmt.Errorf("agentstate: inspect legacy state %s: %w", source, err)
		}
		found = true
		destination := filepath.Join(agentDir, name)
		if err := verifyCopiedTree(source, destination); err != nil {
			return false, fmt.Errorf("agentstate: legacy state still exists at %s but differs from %s: %w", source, destination, err)
		}
	}
	if !found {
		return false, nil
	}
	if err := removeLegacyState(workDir); err != nil {
		return false, fmt.Errorf("agentstate: remove verified legacy state: %w", err)
	}
	return true, nil
}

func removeLegacyState(workDir string) error {
	legacyDir := filepath.Join(workDir, ".juex")
	for _, name := range legacyStateEntries {
		if err := os.RemoveAll(filepath.Join(legacyDir, name)); err != nil {
			return err
		}
	}
	return syncDir(legacyDir)
}

func copyTree(source, destination string) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	switch {
	case info.Mode().IsRegular():
		return copyRegularFile(source, destination, info.Mode().Perm())
	case info.IsDir():
		if err := os.MkdirAll(destination, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyTree(filepath.Join(source, entry.Name()), filepath.Join(destination, entry.Name())); err != nil {
				return err
			}
		}
		if err := os.Chmod(destination, info.Mode().Perm()); err != nil {
			return err
		}
		return syncDir(destination)
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(source)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.Symlink(target, destination)
	default:
		return fmt.Errorf("unsupported file type %s", info.Mode().Type())
	}
}

func copyRegularFile(source, destination string, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(destination)
		}
	}()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

type manifestEntry struct {
	Path       string
	Type       string
	Size       int64
	SHA256     string
	LinkTarget string
}

func compareTree(source, destination string) error {
	sourceManifest, err := buildManifest(source)
	if err != nil {
		return err
	}
	destinationManifest, err := buildManifest(destination)
	if err != nil {
		return err
	}
	if len(sourceManifest) != len(destinationManifest) {
		return fmt.Errorf("manifest entry count %d != %d", len(sourceManifest), len(destinationManifest))
	}
	for i := range sourceManifest {
		if sourceManifest[i] != destinationManifest[i] {
			return fmt.Errorf("manifest mismatch at %q: %+v != %+v", sourceManifest[i].Path, sourceManifest[i], destinationManifest[i])
		}
	}
	return nil
}

func buildManifest(root string) ([]manifestEntry, error) {
	base := filepath.Dir(root)
	var entries []manifestEntry
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		item := manifestEntry{Path: filepath.ToSlash(rel), Size: info.Size()}
		switch {
		case info.IsDir():
			item.Type = "dir"
			item.Size = 0
		case info.Mode().IsRegular():
			item.Type = "file"
			item.SHA256, err = fileSHA256(path)
			if err != nil {
				return err
			}
		case info.Mode()&os.ModeSymlink != 0:
			item.Type = "symlink"
			item.Size = 0
			item.LinkTarget, err = os.Readlink(path)
			if err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported file type at %s: %s", path, info.Mode().Type())
		}
		entries = append(entries, item)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Chmod(perm); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tempPath, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
