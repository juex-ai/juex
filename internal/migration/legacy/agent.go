package legacy

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type AgentDefinition struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Workspace string    `json:"workspace"`
	Enabled   bool      `json:"enabled"`
	Autostart bool      `json:"autostart"`
	CreatedAt time.Time `json:"created_at"`
}

type SkippedSource struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

type Reference struct {
	ThreadID string `json:"thread_id"`
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
}

type Agent struct {
	Definition  AgentDefinition
	Threads     []Thread
	Files       []SourceFile
	AbsentFiles []string
	Skipped     []SkippedSource
	References  []Reference
}

// ReadAgent captures persistent Agent-owned files, including opaque private
// module/extension state. Reading that state is not a claim that its behavior
// has been converted. Workspace files remain outside this ownership boundary.
func ReadAgent(dir string) (Agent, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return Agent{}, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return Agent{}, errors.New("Agent source must be a real directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return Agent{}, err
	}
	defer func() { _ = root.Close() }()
	r := sourceReader{root: root, stats: make(map[string]os.FileInfo)}
	return readAgent(&r, filepath.Base(filepath.Clean(dir)))
}

func readAgent(r *sourceReader, directoryID string) (Agent, error) {
	var agent Agent
	data, err := r.read("agent.json")
	if err != nil {
		return Agent{}, err
	}
	if err := decode(data, &agent.Definition); err != nil {
		return Agent{}, fmt.Errorf("agent.json: %w", err)
	}
	d := agent.Definition
	if !validAgentID(d.ID) || d.ID != directoryID || strings.TrimSpace(d.Name) == "" || !filepath.IsAbs(d.Workspace) || d.CreatedAt.IsZero() {
		return Agent{}, errors.New("agent.json: invalid identity or Workspace binding")
	}
	entries, err := r.list(".", false)
	if err != nil {
		return Agent{}, err
	}
	for _, entry := range entries {
		name := entry.Name()
		switch name {
		case "agent.json", "juex.yaml", "threads.index.json", "observables.json":
			if _, err := r.read(name); err != nil {
				return Agent{}, err
			}
		case "threads":
			if err := agent.readThreads(r, name, false); err != nil {
				return Agent{}, err
			}
		case "archive":
			children, err := r.list(name, false)
			if err != nil {
				return Agent{}, err
			}
			for _, child := range children {
				if child.Name() != "threads" {
					return Agent{}, fmt.Errorf("archive: unsupported source %s", child.Name())
				}
			}
			if err := agent.readThreads(r, "archive/threads", true); err != nil {
				return Agent{}, err
			}
		case "media", "modules", "extensions", "observables":
			if err := agent.captureTree(r, name); err != nil {
				return Agent{}, err
			}
		case ".trash":
			children, err := r.list(name, false)
			if err != nil {
				return Agent{}, err
			}
			for _, child := range children {
				if child.Name() != "threads" {
					return Agent{}, errors.New("Agent .trash contains unknown deletion state")
				}
				items, err := r.list(".trash/threads", false)
				if err != nil {
					return Agent{}, err
				}
				if len(items) != 0 {
					return Agent{}, errors.New("Agent .trash contains unfinished deletion state")
				}
			}
		case ".locks", ".module-resources.lock", "api.sock", "runtime.json", "logs", "tmp":
			agent.Skipped = append(agent.Skipped, SkippedSource{name, "operational process, lock, log or cache state"})
		default:
			return Agent{}, fmt.Errorf("unsupported Agent source path %s", name)
		}
	}
	if err := agent.validateThreads(); err != nil {
		return Agent{}, err
	}
	if err := agent.verifyReferences(r.files); err != nil {
		return Agent{}, err
	}
	if err := r.unchanged(); err != nil {
		return Agent{}, err
	}
	slices.SortFunc(r.files, func(a, b SourceFile) int { return strings.Compare(a.Path, b.Path) })
	agent.Files, agent.AbsentFiles = r.files, r.absent
	return agent, nil
}

func validAgentID(id string) bool {
	return len(id) == 6 && strings.IndexFunc(id, func(c rune) bool { return (c < 'a' || c > 'z') && (c < '2' || c > '7') }) == -1
}

func (a *Agent) readThreads(r *sourceReader, base string, optional bool) error {
	entries, err := r.list(base, optional)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !validThreadID(entry.Name()) {
			return fmt.Errorf("%s: invalid Thread directory %s", base, entry.Name())
		}
		prefix := path.Join(base, entry.Name())
		root, err := r.root.OpenRoot(prefix)
		if err != nil {
			return err
		}
		child := sourceReader{root: root, stats: make(map[string]os.FileInfo)}
		thread, readErr := readThread(&child, entry.Name())
		closeErr := root.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			return fmt.Errorf("%s: %w", prefix, err)
		}
		if (base == "archive/threads") != (thread.Metadata.RetentionState == "archived") {
			return fmt.Errorf("%s: retention disagrees with source directory", prefix)
		}
		if err := r.adopt(prefix, &child); err != nil {
			return err
		}
		if err := a.captureTree(r, prefix); err != nil {
			return err
		}
		a.Threads = append(a.Threads, thread)
	}
	return nil
}

func (a *Agent) captureTree(r *sourceReader, base string) error {
	entries, err := r.list(base, false)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := path.Join(base, entry.Name())
		if operationalTreePath(name) {
			a.Skipped = append(a.Skipped, SkippedSource{name, "operational lock or log state"})
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s: source symlinks are not supported", name)
		}
		if entry.IsDir() {
			if err := a.captureTree(r, name); err != nil {
				return err
			}
		} else {
			if _, err := r.read(name); err != nil {
				return err
			}
		}
	}
	return nil
}

func operationalTreePath(name string) bool {
	if name == "modules/memory/.lock" {
		return true
	}
	parts := strings.Split(name, "/")
	if parts[0] == "archive" {
		parts = parts[1:]
	}
	return len(parts) == 3 && parts[0] == "threads" && validThreadID(parts[1]) && parts[2] == "logs"
}

func (a Agent) validateThreads() error {
	threads := map[string]ThreadMetadata{}
	for _, thread := range a.Threads {
		id := thread.Metadata.ThreadID
		if _, exists := threads[id]; exists {
			return fmt.Errorf("duplicate Thread identity %s", id)
		}
		threads[id] = thread.Metadata
	}
	for id, thread := range threads {
		for otherID, other := range threads {
			if strings.EqualFold(thread.Alias, otherID) || id != otherID && strings.EqualFold(thread.Alias, other.Alias) {
				return fmt.Errorf("Thread %s has an ambiguous alias", id)
			}
		}
		seen := map[string]bool{id: true}
		for thread.ParentThreadID != "" {
			parent, ok := threads[thread.ParentThreadID]
			if !ok || seen[thread.ParentThreadID] {
				return fmt.Errorf("Thread %s has a missing or cyclic parent", id)
			}
			seen[thread.ParentThreadID] = true
			thread = parent
		}
	}
	return nil
}
