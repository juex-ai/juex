package legacy

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
)

// SourceGuard fences the fixed legacy writers using existing lock inodes. It
// does not stop processes or fence external Workspace editors. In particular,
// the old Fleet HTTP recovery handler does not take these locks: the operator
// must stop it and prevent service restart before capturing or importing.
// Use a guard sequentially and hold it until the entire offline apply finishes.
type SourceGuard struct {
	directory string
	root      *os.Root
	agents    []string
	dirs      map[string]os.FileInfo
	locks     []sourceLock
}

type sourceLock struct {
	name string
	info os.FileInfo
	file *os.File
}

// AcquireSourceGuard never creates a file or repairs the legacy source. A busy,
// missing or replaced lock aborts acquisition and releases every acquired lock.
func AcquireSourceGuard(home string, agents []string) (_ *SourceGuard, resultErr error) {
	if !filepath.IsAbs(home) || filepath.Clean(home) != home || len(agents) == 0 {
		return nil, errors.New("source guard requires an absolute Home and complete Agent set")
	}
	agents = slices.Clone(agents)
	slices.Sort(agents)
	for i, id := range agents {
		if !validAgentID(id) || i > 0 && id == agents[i-1] {
			return nil, errors.New("source guard has invalid or duplicate Agent IDs")
		}
	}
	info, err := os.Lstat(home)
	if err != nil {
		return nil, err
	}
	if !sourceGuardDirectory(info) {
		return nil, errors.New("source Home must be a real directory not writable by others")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return nil, err
	}
	g := &SourceGuard{directory: home, root: root, agents: agents, dirs: map[string]os.FileInfo{".": info}}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, g.Close())
		}
	}()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("source Home changed while opening")
	}
	names := []string{"fleet.lock", ".locks/fleet/supervisor-role.lock", ".locks/config-imports-cache.lock", ".locks/services/memory.lock", "services/memory/writer.lock"}
	for _, id := range agents {
		names = append(names, ".locks/fleet/"+id+".lock", ".locks/agents/"+id+".lock", ".locks/endpoints/"+id+".lock", "agents/"+id+"/.module-resources.lock")
	}
	for _, name := range names {
		if err := g.rememberDirectory(path.Dir(name)); err != nil {
			return nil, err
		}
		before, err := root.Lstat(name)
		if err != nil {
			return nil, err
		}
		if !before.Mode().IsRegular() || before.Mode().Perm()&0022 != 0 {
			return nil, fmt.Errorf("%s: invalid source lock", name)
		}
		f, err := openSourceLock(root, name)
		if err != nil {
			return nil, err
		}
		g.locks = append(g.locks, sourceLock{name: name, info: before, file: f})
		opened, err := f.Stat()
		if err != nil || !sameFile(before, opened) {
			return nil, fmt.Errorf("%s: source lock changed while opening", name)
		}
		if err := lockSourceFile(f); err != nil {
			return nil, fmt.Errorf("%s: source writer is busy: %w", name, err)
		}
	}
	if err := g.Check(); err != nil {
		return nil, err
	}
	return g, nil
}

func sourceGuardDirectory(info os.FileInfo) bool {
	return info != nil && info.IsDir() && info.Mode().Perm()&0022 == 0
}

func (g *SourceGuard) rememberDirectory(name string) error {
	if _, ok := g.dirs[name]; ok {
		return nil
	}
	if err := g.rememberDirectory(path.Dir(name)); err != nil {
		return err
	}
	info, err := g.root.Lstat(name)
	if err != nil {
		return err
	}
	if !sourceGuardDirectory(info) {
		return fmt.Errorf("%s: source lock directory must be real and not writable by others", name)
	}
	g.dirs[name] = info
	return nil
}

// Check detects replaced locks/ancestors and changes to the complete Agent set.
// It is a stage-boundary check, not proof that captured files stayed unchanged.
func (g *SourceGuard) Check() error {
	if g == nil || g.root == nil {
		return errors.New("source guard is closed")
	}
	info, err := os.Lstat(g.directory)
	if err != nil || !sourceGuardDirectory(info) || !os.SameFile(g.dirs["."], info) {
		return errors.New("source Home changed")
	}
	for name, before := range g.dirs {
		after, err := g.root.Lstat(name)
		if err != nil || !sourceGuardDirectory(after) || !os.SameFile(before, after) || before.Mode() != after.Mode() {
			return fmt.Errorf("%s: source lock directory changed", name)
		}
	}
	for _, lock := range g.locks {
		after, err := g.root.Lstat(lock.name)
		if err != nil || !sameFile(lock.info, after) {
			return fmt.Errorf("%s: source lock changed", lock.name)
		}
		opened, err := lock.file.Stat()
		if err != nil || !sameFile(lock.info, opened) {
			return fmt.Errorf("%s: held source lock changed", lock.name)
		}
	}
	f, err := openSourceLock(g.root, "agents")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(g.dirs["agents"], opened) {
		return errors.New("source Agent registry changed")
	}
	entries, err := f.ReadDir(-1)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !validAgentID(entry.Name()) {
			return errors.New("source Agent registry contains unsupported state")
		}
		ids = append(ids, entry.Name())
	}
	slices.Sort(ids)
	if !slices.Equal(ids, g.agents) {
		return errors.New("source Agent registry differs from the guarded set")
	}
	return nil
}

// Capture checks the fence around a full read, including Home/Workspace bytes
// and absences. It still requires the operator's independent shutdown evidence.
func (g *SourceGuard) Capture(defaultHome string) (Fleet, error) {
	if err := g.Check(); err != nil {
		return Fleet{}, err
	}
	fleet, err := readFleet(g.root, g.directory, defaultHome)
	if err != nil {
		return Fleet{}, err
	}
	ids := make([]string, 0, len(fleet.Agents))
	for _, agent := range fleet.Agents {
		ids = append(ids, agent.Definition.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, g.agents) {
		return Fleet{}, errors.New("captured Agent registry differs from the guarded set")
	}
	if err := g.Check(); err != nil {
		return Fleet{}, err
	}
	return fleet, nil
}

func (g *SourceGuard) Close() error {
	if g == nil || g.root == nil {
		return nil
	}
	var result error
	for i := len(g.locks) - 1; i >= 0; i-- {
		result = errors.Join(result, g.locks[i].file.Close())
	}
	result = errors.Join(result, g.root.Close())
	g.root, g.locks = nil, nil
	return result
}
