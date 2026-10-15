package extensions

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// ExtensionRuntimeContext carries Agent-owned runtime paths for one selected
// extension without coupling extension discovery to mutable Agent state.
type RuntimeContext struct {
	ExtensionName string
	Source        string
	ExtensionDir  string
	DataDir       string

	agentStateDir string
}

// AgentExtensionsRuntime manages the Agent-owned parent directory used by
// selected extensions for private persistent data.
type RuntimeRoot struct {
	RootDir string

	agentStateDir string
}

func NewRuntimeRoot(stateDir string) RuntimeRoot {
	runtime := RuntimeRoot{agentStateDir: stateDir}
	if stateDir != "" {
		runtime.RootDir = filepath.Join(stateDir, "extensions")
	}
	return runtime
}

// Prepare creates only the Agent-wide root immediately before a sandboxed
// extension child starts. State-free previews remain side-effect free.
func (r RuntimeRoot) Prepare() error {
	if r.RootDir == "" {
		return nil
	}
	if r.agentStateDir == "" {
		return fmt.Errorf("extension runtime: Agent state directory is required")
	}
	statePhysical, err := filepath.EvalSymlinks(r.agentStateDir)
	if err != nil {
		return fmt.Errorf("extension runtime: resolve Agent state directory: %w", err)
	}
	if filepath.Clean(r.RootDir) != filepath.Join(r.agentStateDir, "extensions") {
		return fmt.Errorf("extension runtime: extensions root %q is outside the Agent state directory", r.RootDir)
	}
	if err := ensurePrivateDirectoryWithoutSymlink(r.RootDir); err != nil {
		return fmt.Errorf("extension runtime: prepare extensions root: %w", err)
	}
	rootPhysical, err := filepath.EvalSymlinks(r.RootDir)
	if err != nil {
		return fmt.Errorf("extension runtime: resolve extensions root: %w", err)
	}
	if !pathStrictlyWithin(statePhysical, rootPhysical) {
		return fmt.Errorf("extension runtime: extensions root %q escapes Agent state directory", r.RootDir)
	}
	return nil
}

func NewRuntimeContext(stateDir string, extension Extension) RuntimeContext {
	context := RuntimeContext{
		ExtensionName: extension.Name,
		Source:        extension.Source,
		ExtensionDir:  extension.Dir,
		agentStateDir: stateDir,
	}
	if context.agentStateDir != "" && extension.Name != "" {
		context.DataDir = filepath.Join(context.agentStateDir, "extensions", extension.Name)
	}
	return context
}

// PrepareDataDir creates the persistent data directory immediately before a
// selected local extension process starts. State-free resource previews are a
// deliberate no-op.
func (c RuntimeContext) PrepareDataDir() error {
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

	extensionsRoot := filepath.Join(c.agentStateDir, "extensions")
	if filepath.Clean(c.DataDir) != filepath.Join(extensionsRoot, c.ExtensionName) {
		return fmt.Errorf("extension runtime: data directory %q is outside the Agent extension root", c.DataDir)
	}
	rootRuntime := NewRuntimeRoot(c.agentStateDir)
	if err := rootRuntime.Prepare(); err != nil {
		return err
	}
	rootPhysical, err := filepath.EvalSymlinks(extensionsRoot)
	if err != nil {
		return fmt.Errorf("extension runtime: resolve extensions root: %w", err)
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
	if os.IsNotExist(err) {
		if mkdirErr := os.Mkdir(path, 0o700); mkdirErr != nil && !os.IsExist(mkdirErr) {
			return mkdirErr
		}
		info, err = os.Lstat(path)
	}
	switch {
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
