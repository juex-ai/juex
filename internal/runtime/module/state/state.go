// Package state owns durable Module resource ownership and retirement. It never
// interprets resource bodies or depends on an installed Module implementation.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/juex-ai/juex/internal/homestore"
)

type Scope string

const (
	ScopeRuntime Scope = "runtime"
	ScopeThread  Scope = "thread"
)

type Retention string

const (
	Retain           Retention = "retain"
	DiscardOnRemoval Retention = "discard-on-removal"
	ledgerFile                 = "module-resources.json"
	intentFile                 = "module-retirements.json"
)

type Owner struct {
	Module string `json:"module"`
	Scope  Scope  `json:"scope"`
}
type resource struct {
	Owner
	Path      string    `json:"path"`
	Retention Retention `json:"retention"`
}
type ledger struct {
	Version   int        `json:"version"`
	Resources []resource `json:"resources"`
}
type intent struct {
	Version int     `json:"version"`
	Owners  []Owner `json:"owners"`
}

var resourceMu sync.Mutex

func relativePath(owner Owner) string { return "modules/" + owner.Module }

// Directory is a pure locator. Prepare must succeed before writing beneath it.
func Directory(scopeDir string, owner Owner) string {
	return filepath.Join(scopeDir, filepath.FromSlash(relativePath(owner)))
}

func validateOwner(owner Owner) error {
	if owner.Scope != ScopeThread {
		return fmt.Errorf("module state: unsupported resource scope %q", owner.Scope)
	}
	if owner.Module == "" || strings.TrimSpace(owner.Module) != owner.Module || owner.Module == "." || owner.Module == ".." || strings.ContainsAny(owner.Module, `/\\:`) {
		return fmt.Errorf("module state: invalid owner %q", owner.Module)
	}
	for _, r := range owner.Module {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') && r != '-' {
			return fmt.Errorf("module state: invalid owner %q", owner.Module)
		}
	}
	return nil
}

// Prepare durably records ownership before a resource directory can be written.
// An interrupted registration may leave a missing directory, which is safe to
// retry. Existing unrecorded directories are never automatically adopted.
func Prepare(scopeDir string, owner Owner, retention Retention) (string, error) {
	resourceMu.Lock()
	defer resourceMu.Unlock()
	if strings.TrimSpace(scopeDir) == "" {
		return "", errors.New("module state: scope directory is required")
	}
	if err := validateOwner(owner); err != nil {
		return "", err
	}
	if retention != Retain && retention != DiscardOnRemoval {
		return "", errors.New("module state: invalid retention")
	}
	records, err := readLedger(scopeDir)
	if err != nil {
		return "", err
	}
	found := false
	for _, r := range records.Resources {
		if r.Owner == owner {
			if r.Retention != retention {
				return "", errors.New("module state: conflicting retention")
			}
			found = true
		}
	}
	dir := Directory(scopeDir, owner)
	if !found {
		if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("module state: refusing to adopt unrecorded directory %s: %w", dir, errors.Join(err, os.ErrExist))
		}
		records.Resources = append(records.Resources, resource{Owner: owner, Path: relativePath(owner), Retention: retention})
		if err := writeJSON(filepath.Join(scopeDir, ledgerFile), records); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := homestore.SyncDir(dir); err != nil {
		return "", err
	}
	if err := homestore.SyncDir(filepath.Dir(dir)); err != nil {
		return "", err
	}
	if err := homestore.SyncDir(scopeDir); err != nil {
		return "", err
	}
	return dir, nil
}

func readLedger(scopeDir string) (ledger, error) {
	result := ledger{Version: 1}
	if err := plainDirectory(scopeDir); err != nil {
		return result, err
	}
	if err := plainDirectory(filepath.Join(scopeDir, "modules")); err != nil {
		return result, err
	}
	if err := readJSON(filepath.Join(scopeDir, ledgerFile), &result); err != nil {
		return result, err
	}
	if result.Version != 1 {
		return result, errors.New("module state: unsupported ownership version")
	}
	seen := map[Owner]bool{}
	for _, r := range result.Resources {
		if err := validateOwner(r.Owner); err != nil {
			return result, err
		}
		if r.Path != relativePath(r.Owner) || seen[r.Owner] || (r.Retention != Retain && r.Retention != DiscardOnRemoval) {
			return result, errors.New("module state: invalid or conflicting resource ownership")
		}
		seen[r.Owner] = true
		if err := plainDirectory(Directory(scopeDir, r.Owner)); err != nil {
			return result, err
		}
	}
	return result, nil
}

func plainDirectory(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("module state: expected real directory: %s", path)
	}
	return nil
}
func readJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("module state: expected regular metadata: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("module state metadata %s: %w", path, err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("module state: trailing metadata in %s", path)
	}
	return nil
}
func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return homestore.WriteFileAtomic(path, append(data, '\n'), 0600, 0700)
}

// Reconcile applies an accepted composition while all previous resource writers
// are stopped. scopeDirs must enumerate active and archived Thread directories
// without opening or recovering Threads. Callers hold the Agent lifecycle lease.
func Reconcile(agentDir string, scopeDirs []string, enabled []Owner) error {
	return reconcile(agentDir, scopeDirs, enabled, removeOwnedDirectory)
}
func reconcile(agentDir string, scopeDirs []string, enabled []Owner, remove func(string) error) error {
	resourceMu.Lock()
	defer resourceMu.Unlock()
	pending := intent{Version: 1}
	if err := readJSON(filepath.Join(agentDir, intentFile), &pending); err != nil {
		return err
	}
	if pending.Version != 1 {
		return errors.New("module state: unsupported retirement version")
	}
	for _, owner := range append(slices.Clone(enabled), pending.Owners...) {
		if err := validateOwner(owner); err != nil {
			return err
		}
	}
	records := make([]ledger, len(scopeDirs))
	for i, dir := range scopeDirs {
		var err error
		records[i], err = readLedger(dir)
		if err != nil {
			return err
		}
		for _, r := range records[i].Resources {
			if r.Retention == DiscardOnRemoval && !slices.Contains(enabled, r.Owner) && !slices.Contains(pending.Owners, r.Owner) {
				pending.Owners = append(pending.Owners, r.Owner)
			}
		}
	}
	if len(pending.Owners) == 0 {
		return nil
	}
	// Persist the complete operation before deleting any Thread resource. A new
	// configuration cannot cancel this intent after a partial cleanup or crash.
	if err := writeJSON(filepath.Join(agentDir, intentFile), pending); err != nil {
		return err
	}
	for i, dir := range scopeDirs {
		for _, r := range slices.Clone(records[i].Resources) {
			if r.Retention != DiscardOnRemoval || !slices.Contains(pending.Owners, r.Owner) {
				continue
			}
			if err := remove(Directory(dir, r.Owner)); err != nil {
				return fmt.Errorf("module retirement pending for %s in %s: %w", r.Module, dir, err)
			}
			if err := syncExistingDir(filepath.Join(dir, "modules")); err != nil {
				return err
			}
			records[i].Resources = slices.DeleteFunc(records[i].Resources, func(candidate resource) bool { return candidate.Owner == r.Owner })
			if err := writeJSON(filepath.Join(dir, ledgerFile), records[i]); err != nil {
				return err
			}
		}
	}
	if err := os.Remove(filepath.Join(agentDir, intentFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return homestore.SyncDir(agentDir)
}
func syncExistingDir(path string) error {
	err := homestore.SyncDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return homestore.SyncDir(filepath.Dir(path))
	}
	return err
}

// Anchor deletion to the scope directory so a concurrent external symlink
// replacement cannot redirect retirement outside the recorded Thread.
func removeOwnedDirectory(path string) error {
	root, err := os.OpenRoot(filepath.Dir(filepath.Dir(path)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat("modules")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("module state: linked resource parent")
	}
	ownedRoot, err := root.OpenRoot("modules")
	if err != nil {
		return err
	}
	defer func() { _ = ownedRoot.Close() }()
	opened, err := ownedRoot.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return errors.New("module state: resource parent changed during retirement")
	}
	return ownedRoot.RemoveAll(filepath.Base(path))
}
