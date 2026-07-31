package app

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/extensions"
)

// ExtensionRuntimeContext carries Agent-owned runtime paths for one selected
// extension without coupling extension discovery to mutable Agent state.
type ExtensionRuntimeContext struct {
	ExtensionName string
	Source        string
	ExtensionDir  string
	DataDir       string

	agentStateDir string
}

func newExtensionRuntimeContext(address agentstate.AgentAddress, extension extensions.Extension) ExtensionRuntimeContext {
	context := ExtensionRuntimeContext{
		ExtensionName: extension.Name,
		Source:        extension.Source,
		ExtensionDir:  extension.Dir,
		agentStateDir: address.StateDir(),
	}
	if context.agentStateDir != "" && extension.Name != "" {
		context.DataDir = filepath.Join(context.agentStateDir, "extensions", extension.Name)
	}
	return context
}

// AdditionalWritableRoots returns the narrow Agent-owned path that an
// extension-aware sandbox request may explicitly grant.
func (c ExtensionRuntimeContext) AdditionalWritableRoots() []string {
	if c.DataDir == "" {
		return nil
	}
	return []string{c.DataDir}
}

// PrepareDataDir creates the persistent data directory immediately before a
// selected local extension process starts. State-free resource previews are a
// deliberate no-op.
func (c ExtensionRuntimeContext) PrepareDataDir() error {
	if c.DataDir == "" {
		return nil
	}
	if c.agentStateDir == "" {
		return fmt.Errorf("extension runtime: Agent state directory is required")
	}
	if c.ExtensionName == "" ||
		c.ExtensionName == "." ||
		c.ExtensionName == ".." ||
		filepath.Base(c.ExtensionName) != c.ExtensionName ||
		strings.ContainsAny(c.ExtensionName, `/\`) {
		return fmt.Errorf("extension runtime: invalid extension name %q", c.ExtensionName)
	}

	statePhysical, err := filepath.EvalSymlinks(c.agentStateDir)
	if err != nil {
		return fmt.Errorf("extension runtime: resolve Agent state directory: %w", err)
	}
	extensionsRoot := filepath.Join(c.agentStateDir, "extensions")
	if filepath.Clean(c.DataDir) != filepath.Join(extensionsRoot, c.ExtensionName) {
		return fmt.Errorf("extension runtime: data directory %q is outside the Agent extension root", c.DataDir)
	}
	if err := ensurePrivateDirectoryWithoutSymlink(extensionsRoot); err != nil {
		return fmt.Errorf("extension runtime: prepare extensions root: %w", err)
	}
	rootPhysical, err := filepath.EvalSymlinks(extensionsRoot)
	if err != nil {
		return fmt.Errorf("extension runtime: resolve extensions root: %w", err)
	}
	if !pathStrictlyWithin(statePhysical, rootPhysical) {
		return fmt.Errorf("extension runtime: extensions root %q escapes Agent state directory", extensionsRoot)
	}

	if err := ensurePrivateDirectoryWithoutSymlink(c.DataDir); err != nil {
		return fmt.Errorf("extension runtime: prepare data directory: %w", err)
	}
	dataPhysical, err := filepath.EvalSymlinks(c.DataDir)
	if err != nil {
		return fmt.Errorf("extension runtime: resolve data directory: %w", err)
	}
	if !pathStrictlyWithin(rootPhysical, dataPhysical) {
		return fmt.Errorf("extension runtime: data directory %q escapes Agent extension root", c.DataDir)
	}
	return nil
}

func ensurePrivateDirectoryWithoutSymlink(path string) error {
	info, err := os.Lstat(path)
	switch {
	case os.IsNotExist(err):
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
	case err != nil:
		return err
	case info.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%q must not be a symlink", path)
	case !info.IsDir():
		return fmt.Errorf("%q is not a directory", path)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o700); err != nil {
			return err
		}
	}
	return nil
}

func pathStrictlyWithin(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
