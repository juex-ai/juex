package fleetservice

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

func publishFiles(files []definitionFile) error {
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
		if err := writeFileAtomic(file.path, file.data, file.mode); err != nil {
			return errors.Join(err, rollbackFiles(snapshots[:i]), rollbackDirectories(missingDirs))
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
			rollbackErr = errors.Join(rollbackErr, writeFileAtomic(snapshot.path, snapshot.data, snapshot.mode))
			continue
		}
		if err := os.Remove(snapshot.path); err != nil && !errors.Is(err, os.ErrNotExist) {
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
		if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
			rollbackErr = errors.Join(rollbackErr, fmt.Errorf("fleet service: roll back definition directory %s: %w", dir, err))
		}
	}
	return rollbackErr
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("fleet service: create definition directory %s: %w", dir, err)
	}
	temp, err := os.CreateTemp(dir, ".juex-fleet-service-*")
	if err != nil {
		return fmt.Errorf("fleet service: create temporary definition: %w", err)
	}
	tempPath := temp.Name()
	cleanup := func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}
	if err := temp.Chmod(mode); err != nil {
		cleanup()
		return fmt.Errorf("fleet service: chmod temporary definition: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("fleet service: write temporary definition: %w", err)
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("fleet service: sync temporary definition: %w", err)
	}
	if err := temp.Close(); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("fleet service: close temporary definition: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("fleet service: publish definition %s: %w", path, err)
	}
	return nil
}
