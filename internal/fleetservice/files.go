package fleetservice

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/juex-ai/juex/internal/homestore"
)

func publishFiles(files []definitionFile) error {
	return publishFilesWith(files, publishDefinitionFile)
}

func publishFilesWith(files []definitionFile, publish func(string, []byte, os.FileMode) error) error {
	snapshots := make([]fileSnapshot, len(files))
	missingDirs := make(map[string]struct{})
	for i, file := range files {
		snapshot, err := snapshotFile(file.path)
		if err != nil {
			return err
		}
		snapshots[i] = snapshot
		dirs, err := missingParentDirectories(file.path)
		if err != nil {
			return err
		}
		for _, dir := range dirs {
			missingDirs[dir] = struct{}{}
		}
	}
	for i, file := range files {
		if err := publish(file.path, file.data, file.mode); err != nil {
			rollbackCount := i
			if homestore.ReplacementOccurred(err) {
				rollbackCount++
			}
			return errors.Join(err, rollbackFiles(snapshots[:rollbackCount]), rollbackDirectories(missingDirs))
		}
	}
	return nil
}

type fileSnapshot struct {
	path    string
	data    []byte
	mode    os.FileMode
	existed bool
}

func snapshotFile(path string) (fileSnapshot, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fileSnapshot{path: path}, nil
	}
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("fleet service: inspect existing definition %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fileSnapshot{}, fmt.Errorf("fleet service: existing definition %s is not a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fileSnapshot{}, fmt.Errorf("fleet service: read existing definition %s: %w", path, err)
	}
	return fileSnapshot{path: path, data: data, mode: info.Mode().Perm(), existed: true}, nil
}

func rollbackFiles(snapshots []fileSnapshot) error {
	var rollbackErr error
	for i := len(snapshots) - 1; i >= 0; i-- {
		snapshot := snapshots[i]
		if snapshot.existed {
			rollbackErr = errors.Join(rollbackErr, publishDefinitionFile(snapshot.path, snapshot.data, snapshot.mode))
			continue
		}
		if err := removeDurably(snapshot.path); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("fleet service: roll back definition %s: %w", snapshot.path, err))
		}
	}
	return rollbackErr
}

func missingParentDirectories(path string) ([]string, error) {
	var missing []string
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		_, err := os.Stat(dir)
		if err == nil {
			return missing, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("fleet service: inspect definition directory %s: %w", dir, err)
		}
		missing = append(missing, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			return missing, nil
		}
	}
}

func rollbackDirectories(missing map[string]struct{}) error {
	dirs := make([]string, 0, len(missing))
	for dir := range missing {
		dirs = append(dirs, dir)
	}
	slices.SortFunc(dirs, func(a, b string) int { return len(b) - len(a) })
	var rollbackErr error
	for _, dir := range dirs {
		if err := removeDurably(dir); err != nil {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("fleet service: roll back definition directory %s: %w", dir, err))
		}
	}
	return rollbackErr
}

func removeDurably(path string) error {
	return removeDurablyWith(path, os.Remove, homestore.SyncDir)
}

func removeDurablyWith(path string, remove, syncDir func(string) error) error {
	if err := remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return syncDir(filepath.Dir(path))
}

func publishDefinitionFile(path string, data []byte, mode os.FileMode) error {
	if err := homestore.WriteFileAtomic(path, data, mode, 0o700); err != nil {
		return fmt.Errorf("fleet service: publish definition %s: %w", path, err)
	}
	return nil
}
