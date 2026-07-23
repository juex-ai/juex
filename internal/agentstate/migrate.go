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

	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/homestore"
)

var legacyStateEntries = []string{"sessions", "memory", "history.json", "logs", "observables"}

var verifyCopiedTree = compareTree

func publishNewAgent(address AgentAddress, workDir string, agent Agent) (migrated bool, err error) {
	agentDir := address.StateDir()
	agentsDir := filepath.Dir(agentDir)
	stageDir, err := os.MkdirTemp(agentsDir, "."+agent.ID+".creating-")
	if err != nil {
		return false, fmt.Errorf("agentstate: create staging directory: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(stageDir)
		}
	}()

	for _, name := range []string{"sessions", "memory", "logs"} {
		if err := os.MkdirAll(filepath.Join(stageDir, name), 0o755); err != nil {
			return false, err
		}
	}
	legacyDir := filepath.Join(workDir, ".juex")
	for _, name := range legacyStateEntries {
		source := filepath.Join(legacyDir, name)
		if _, statErr := os.Lstat(source); statErr != nil {
			if errors.Is(statErr, os.ErrNotExist) {
				continue
			}
			return false, fmt.Errorf("agentstate: inspect legacy state %s: %w", source, statErr)
		}
		migrated = true
		destination := filepath.Join(stageDir, name)
		if err := os.RemoveAll(destination); err != nil {
			return false, err
		}
		if err := copyTree(source, destination); err != nil {
			return false, fmt.Errorf("agentstate: copy legacy state %s: %w", source, err)
		}
		if err := verifyCopiedTree(source, destination); err != nil {
			return false, fmt.Errorf("agentstate: verify legacy state %s: %w", source, err)
		}
	}
	for _, name := range []string{"sessions", "memory", "logs"} {
		if err := os.MkdirAll(filepath.Join(stageDir, name), 0o755); err != nil {
			return false, err
		}
	}
	historyPath := filepath.Join(stageDir, "history.json")
	if _, statErr := os.Stat(historyPath); errors.Is(statErr, os.ErrNotExist) {
		if err := homestore.WriteFileAtomic(historyPath, []byte("{\"sessions\":[]}\n"), 0o644, 0o755); err != nil {
			return false, err
		}
	} else if statErr != nil {
		return false, statErr
	}
	if err := atomicWriteJSON(filepath.Join(stageDir, agentFileName), agent, 0o644); err != nil {
		return false, err
	}
	if err := homestore.SyncDir(stageDir); err != nil {
		return false, fmt.Errorf("agentstate: sync staging directory: %w", err)
	}
	if err := os.Rename(stageDir, agentDir); err != nil {
		return false, fmt.Errorf("agentstate: publish agent directory %s: %w", agentDir, err)
	}
	if err := homestore.SyncDir(agentsDir); err != nil {
		_ = os.RemoveAll(agentDir)
		_ = homestore.SyncDir(agentsDir)
		return false, fmt.Errorf("agentstate: sync agent registry: %w", err)
	}
	return migrated, nil
}

func migratePublishedLegacyState(workDir string, address AgentAddress) (bool, error) {
	legacyDir := filepath.Join(workDir, ".juex")
	var entries []string
	for _, name := range legacyStateEntries {
		source := filepath.Join(legacyDir, name)
		if _, err := os.Lstat(source); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return false, fmt.Errorf("agentstate: inspect legacy state %s: %w", source, err)
		}
		entries = append(entries, name)
	}
	if len(entries) == 0 {
		return false, nil
	}

	maintenance, err := endpoint.AcquireMaintenance(address)
	if err != nil {
		return false, fmt.Errorf("agentstate: acquire maintenance guard before migrating workspace runtime state: %w", err)
	}
	defer func() { _ = maintenance.Close() }()

	for _, name := range entries {
		source := filepath.Join(legacyDir, name)
		destination := filepath.Join(address.StateDir(), name)
		if _, err := os.Lstat(destination); errors.Is(err, os.ErrNotExist) {
			if err := publishLegacyEntry(source, destination); err != nil {
				return false, err
			}
		} else if err != nil {
			return false, fmt.Errorf("agentstate: inspect migrated state %s: %w", destination, err)
		}
		if err := verifyCopiedTree(source, destination); err != nil {
			return false, fmt.Errorf("agentstate: legacy state still exists at %s but differs from %s: %w", source, destination, err)
		}
	}
	if err := removeLegacyState(workDir); err != nil {
		return false, fmt.Errorf("agentstate: remove verified legacy state: %w", err)
	}
	return true, nil
}

func publishLegacyEntry(source, destination string) (err error) {
	parent := filepath.Dir(destination)
	stageDir, err := os.MkdirTemp(parent, "."+filepath.Base(destination)+".migrating-")
	if err != nil {
		return fmt.Errorf("agentstate: create migration stage for %s: %w", destination, err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(stageDir); err == nil && cleanupErr != nil {
			err = cleanupErr
		}
	}()

	staged := filepath.Join(stageDir, filepath.Base(destination))
	if err := copyTree(source, staged); err != nil {
		return fmt.Errorf("agentstate: copy legacy state %s: %w", source, err)
	}
	if err := verifyCopiedTree(source, staged); err != nil {
		return fmt.Errorf("agentstate: verify legacy state %s: %w", source, err)
	}
	if err := homestore.SyncDir(stageDir); err != nil {
		return fmt.Errorf("agentstate: sync migration stage for %s: %w", destination, err)
	}
	if _, err := os.Lstat(destination); err == nil {
		if err := verifyCopiedTree(source, destination); err != nil {
			return fmt.Errorf("agentstate: migration destination %s appeared with conflicting state: %w", destination, err)
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("agentstate: inspect migration destination %s: %w", destination, err)
	}
	if err := os.Rename(staged, destination); err != nil {
		return fmt.Errorf("agentstate: publish migrated state %s: %w", destination, err)
	}
	if err := homestore.SyncDir(parent); err != nil {
		return fmt.Errorf("agentstate: sync migrated state directory %s: %w", parent, err)
	}
	return nil
}

func removeLegacyState(workDir string) error {
	legacyDir := filepath.Join(workDir, ".juex")
	for _, name := range legacyStateEntries {
		path := filepath.Join(legacyDir, name)
		if err := makeDirectoriesRemovable(path); err != nil {
			return err
		}
		if err := os.RemoveAll(path); err != nil {
			return err
		}
	}
	return homestore.SyncDir(legacyDir)
}

func makeDirectoriesRemovable(root string) error {
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		return os.Chmod(path, info.Mode().Perm()|0o700)
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
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
		if err := os.MkdirAll(destination, info.Mode().Perm()|0o700); err != nil {
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
		return homestore.SyncDir(destination)
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
		info, err := entry.Info()
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
