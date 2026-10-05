package legacy

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

type Workspace struct {
	AgentID      string
	Path         string
	ResolvedPath string
	Files        []SourceFile
	AbsentFiles  []string
}

type HomeConfig struct {
	Directory   string
	Files       []SourceFile
	AbsentFiles []string
}

type Fleet struct {
	SourceHome  string
	DefaultHome HomeConfig
	ID          string
	Agents      []Agent
	Memory      *Memory
	Workspaces  []Workspace
	Files       []SourceFile
	AbsentFiles []string
	Skipped     []SkippedSource
}

// ReadFleet captures the owner stores and source configuration layers. The
// source user's default Home must be explicit, including when it did not exist;
// the migration process's HOME cannot identify the source user's configuration.
// It deliberately does not absorb arbitrary Workspace trees into platform-owned
// storage. Cutover still requires their separate verified backup and a stopped
// source; the stability checks here are not a cross-process transaction.
func ReadFleet(home, defaultHome string) (Fleet, error) {
	home, err := filepath.Abs(home)
	if err != nil {
		return Fleet{}, err
	}
	info, err := os.Lstat(home)
	if err != nil {
		return Fleet{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Fleet{}, errors.New("Fleet source must be a real directory")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return Fleet{}, err
	}
	defer func() { _ = root.Close() }()
	r := sourceReader{root: root, stats: make(map[string]os.FileInfo)}
	fleet := Fleet{SourceHome: home}
	defaultConfig, defaultReader, err := readDefaultHomeConfig(defaultHome)
	if err != nil {
		return Fleet{}, err
	}
	defer func() { _ = defaultReader.root.Close() }()
	fleet.DefaultHome = defaultConfig
	if err := captureConfigImports(&r, &fleet); err != nil {
		return Fleet{}, err
	}
	data, err := r.read("fleet.json")
	if err != nil {
		return Fleet{}, err
	}
	var identity struct {
		ID string `json:"id"`
	}
	if err := decode(data, &identity); err != nil {
		return Fleet{}, err
	}
	if identity.ID == "" || strings.TrimSpace(identity.ID) != identity.ID {
		return Fleet{}, errors.New("invalid Fleet identity")
	}
	fleet.ID = identity.ID
	if _, err := r.optionalRead("juex.yaml"); err != nil {
		return Fleet{}, err
	}
	entries, err := r.list(".", false)
	if err != nil {
		return Fleet{}, err
	}
	for _, entry := range entries {
		name := entry.Name()
		switch name {
		case "fleet.json", "juex.yaml":
		case "agents":
			agents, err := r.list(name, false)
			if err != nil {
				return Fleet{}, err
			}
			for _, item := range agents {
				if !item.IsDir() || item.Type()&os.ModeSymlink != 0 || !validAgentID(item.Name()) {
					return Fleet{}, fmt.Errorf("agents/%s: invalid or unfinished Agent state", item.Name())
				}
				prefix := path.Join("agents", item.Name())
				child, err := r.child(prefix)
				if err != nil {
					return Fleet{}, err
				}
				agent, readErr := readAgent(child, item.Name())
				if err := errors.Join(readErr, child.root.Close()); err != nil {
					return Fleet{}, fmt.Errorf("%s: %w", prefix, err)
				}
				if err := r.adopt(prefix, child); err != nil {
					return Fleet{}, err
				}
				for _, skipped := range agent.Skipped {
					skipped.Path = path.Join(prefix, skipped.Path)
					fleet.Skipped = append(fleet.Skipped, skipped)
				}
				fleet.Agents = append(fleet.Agents, agent)
			}
		case "fleet":
			items, err := r.list(name, false)
			if err != nil {
				return Fleet{}, err
			}
			for _, item := range items {
				if item.Name() != "supervisor.json" {
					return Fleet{}, fmt.Errorf("fleet/%s: unsupported Fleet state", item.Name())
				}
				if _, err := r.read("fleet/supervisor.json"); err != nil {
					return Fleet{}, err
				}
			}
		case "services":
			services, err := r.list(name, false)
			if err != nil {
				return Fleet{}, err
			}
			for _, service := range services {
				if service.Name() != "memory" || !service.IsDir() || service.Type()&os.ModeSymlink != 0 {
					return Fleet{}, fmt.Errorf("services/%s: unsupported service state", service.Name())
				}
				child, err := r.child("services/memory")
				if err != nil {
					return Fleet{}, err
				}
				memory, readErr := readMemory(child, fleet.ID)
				if err := errors.Join(readErr, child.root.Close()); err != nil {
					return Fleet{}, err
				}
				if err := r.adopt("services/memory", child); err != nil {
					return Fleet{}, err
				}
				for _, skipped := range memory.Skipped {
					skipped.Path = path.Join("services/memory", skipped.Path)
					fleet.Skipped = append(fleet.Skipped, skipped)
				}
				fleet.Memory = &memory
			}
		case "cache":
		case ".locks", "fleet.lock", "backups", "logs", "run", "workspaces":
			fleet.Skipped = append(fleet.Skipped, SkippedSource{name, "operational files, existing backups or separately owned Workspaces"})
		default:
			return Fleet{}, fmt.Errorf("unsupported Fleet source path %s", name)
		}
	}
	var workspaceReaders []*sourceReader
	defer func() {
		for _, reader := range workspaceReaders {
			_ = reader.root.Close()
		}
	}()
	for _, agent := range fleet.Agents {
		workspace, reader, err := readWorkspaceConfig(agent.Definition)
		if err != nil {
			return Fleet{}, fmt.Errorf("Agent %s Workspace: %w", agent.Definition.ID, err)
		}
		fleet.Workspaces = append(fleet.Workspaces, workspace)
		workspaceReaders = append(workspaceReaders, reader)
	}
	for i, reader := range workspaceReaders {
		if err := reader.unchanged(); err != nil {
			return Fleet{}, err
		}
		current, err := os.Stat(fleet.Workspaces[i].Path)
		if err != nil || !sameFile(reader.stats["."], current) {
			return Fleet{}, errors.New("Workspace binding changed during capture")
		}
	}
	if err := r.unchanged(); err != nil {
		return Fleet{}, err
	}
	if err := defaultReader.unchanged(); err != nil {
		return Fleet{}, err
	}
	slices.SortFunc(r.files, func(a, b SourceFile) int { return strings.Compare(a.Path, b.Path) })
	fleet.Files, fleet.AbsentFiles = r.files, r.absent
	return fleet, nil
}

func readDefaultHomeConfig(directory string) (HomeConfig, *sourceReader, error) {
	if !filepath.IsAbs(directory) {
		return HomeConfig{}, nil, errors.New("source user's default Home must be an explicit absolute directory")
	}
	directory = filepath.Clean(directory)
	root, err := os.OpenRoot(filepath.Dir(directory))
	if err != nil {
		return HomeConfig{}, nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = root.Close()
		}
	}()
	base := filepath.Base(directory)
	info, err := root.Lstat(base)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return HomeConfig{}, nil, err
	}
	if info != nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
		return HomeConfig{}, nil, errors.New("source default Home must be a real directory or absent")
	}
	r := &sourceReader{root: root, stats: map[string]os.FileInfo{base: info}}
	data, err := r.optionalRead(path.Join(base, "juex.yaml"))
	if err != nil {
		return HomeConfig{}, nil, err
	}
	config := HomeConfig{Directory: directory}
	if data == nil {
		config.AbsentFiles = []string{"juex.yaml"}
	} else {
		file := r.files[0]
		file.Path = "juex.yaml"
		config.Files = []SourceFile{file}
	}
	failed = false
	return config, r, nil
}

func captureConfigImports(r *sourceReader, fleet *Fleet) error {
	const journal = "cache/config-imports/.publication-journal.json"
	if data, err := r.optionalRead(journal); err != nil {
		return err
	} else if data != nil {
		return errors.New("configuration has an unresolved publication journal; source recovery is required")
	}
	entries, err := r.list("cache", true)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := path.Join("cache", entry.Name())
		if entry.Name() != "config-imports" {
			fleet.Skipped = append(fleet.Skipped, SkippedSource{name, "operational cache"})
			continue
		}
		files, err := r.list(name, false)
		if err != nil {
			return err
		}
		for _, file := range files {
			if file.Name() == ".publication-journal.json" {
				return errors.New("configuration publication began during capture")
			}
			// Preserve imported configuration bytes independently of network
			// availability. Selection and conversion happen after this capture.
			if _, err := r.read(path.Join(name, file.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *sourceReader) child(prefix string) (*sourceReader, error) {
	root, err := r.root.OpenRoot(prefix)
	if err != nil {
		return nil, err
	}
	return &sourceReader{root: root, stats: make(map[string]os.FileInfo)}, nil
}

func readWorkspaceConfig(agent AgentDefinition) (Workspace, *sourceReader, error) {
	resolved, err := filepath.EvalSymlinks(agent.Workspace)
	if err != nil {
		return Workspace{}, nil, err
	}
	root, err := os.OpenRoot(resolved)
	if err != nil {
		return Workspace{}, nil, err
	}
	r := &sourceReader{root: root, stats: make(map[string]os.FileInfo)}
	failed := true
	defer func() {
		if failed {
			_ = root.Close()
		}
	}()
	if _, err := r.list(".", false); err != nil {
		return Workspace{}, nil, err
	}
	config := ".juex/juex.yaml"
	if filepath.Base(filepath.Clean(agent.Workspace)) == ".juex" {
		config = "juex.yaml"
	}
	for _, name := range []string{config, ".env", "AGENTS.md", ".agents/AGENTS.md"} {
		if _, err := r.optionalRead(name); err != nil {
			return Workspace{}, nil, err
		}
	}
	failed = false
	return Workspace{AgentID: agent.ID, Path: agent.Workspace, ResolvedPath: resolved, Files: r.files, AbsentFiles: r.absent}, r, nil
}
