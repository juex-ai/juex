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

// ExtensionResources records which source modules permit resource discovery.
// A disabled category is not probed and cannot be reported as absent.
type ExtensionResources struct {
	MCP         bool `json:"mcp"`
	Hooks       bool `json:"hooks"`
	Observables bool `json:"observables"`
	Skills      bool `json:"skills"`
}

type ExtensionSnapshot struct {
	Directory   string             `json:"directory"`
	Selection   ExtensionResources `json:"selection"`
	Files       []SourceFile       `json:"files"`
	AbsentFiles []string           `json:"absent_files"`
}

// ReadExtension captures an explicitly selected installation, not its private
// Agent data. The caller proves allow policy and the winning source root before
// calling; this reader never discovers roots, merges bundles or runs commands.
// Symlinks require separate proven capture and are rejected here even where the
// source loader allowed them. Captured bytes alone do not establish conversion.
func ReadExtension(directory string, selection ExtensionResources) (ExtensionSnapshot, error) {
	snapshot, reader, err := readExtension(directory, selection)
	if err != nil {
		return ExtensionSnapshot{}, err
	}
	defer func() { _ = reader.root.Close() }()
	if err := extensionUnchanged(directory, reader); err != nil {
		return ExtensionSnapshot{}, err
	}
	return snapshot, nil
}

func readExtension(directory string, selection ExtensionResources) (ExtensionSnapshot, *sourceReader, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || strings.ContainsRune(directory, 0) {
		return ExtensionSnapshot{}, nil, errors.New("extension capture requires an explicit absolute directory")
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return ExtensionSnapshot{}, nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ExtensionSnapshot{}, nil, errors.New("extension capture requires a real directory")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return ExtensionSnapshot{}, nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = root.Close()
		}
	}()
	r := &sourceReader{root: root, stats: map[string]os.FileInfo{".": info}}
	entries, err := r.list(".", false)
	if err != nil {
		return ExtensionSnapshot{}, nil, err
	}
	if err := extensionFile(r, "juex.extension.json", entries, true); err != nil {
		return ExtensionSnapshot{}, nil, err
	}
	for _, resource := range []struct {
		name    string
		enabled bool
	}{{"mcp.json", selection.MCP}, {"hooks.yaml", selection.Hooks}, {"observables.json", selection.Observables}} {
		if resource.enabled {
			if err := extensionFile(r, resource.name, entries, false); err != nil {
				return ExtensionSnapshot{}, nil, err
			}
		}
	}
	if selection.Skills {
		if err := extensionCase("skills", entries); err != nil {
			return ExtensionSnapshot{}, nil, err
		}
		skills, err := r.list("skills", true)
		if err != nil {
			return ExtensionSnapshot{}, nil, err
		}
		for _, skill := range skills {
			if skill.Type()&os.ModeSymlink != 0 {
				return ExtensionSnapshot{}, nil, errors.New("extension skill symlinks require separate capture")
			}
			if !skill.IsDir() {
				continue
			}
			name := path.Join("skills", skill.Name())
			files, err := r.list(name, false)
			if err != nil {
				return ExtensionSnapshot{}, nil, err
			}
			if err := extensionFile(r, path.Join(name, "SKILL.md"), files, false); err != nil {
				return ExtensionSnapshot{}, nil, err
			}
		}
	}
	slices.SortFunc(r.files, func(a, b SourceFile) int { return strings.Compare(a.Path, b.Path) })
	slices.Sort(r.absent)
	failed = false
	return ExtensionSnapshot{Directory: directory, Selection: selection, Files: r.files, AbsentFiles: r.absent}, r, nil
}

func extensionFile(r *sourceReader, name string, entries []os.DirEntry, required bool) error {
	if err := extensionCase(path.Base(name), entries); err != nil {
		return err
	}
	if required {
		_, err := r.read(name)
		return err
	}
	_, err := r.optionalRead(name)
	return err
}

// Case conflicts are ambiguous on case-insensitive filesystems. The source
// manifest discovery compared directory entry names, not only open success.
func extensionCase(name string, entries []os.DirEntry) error {
	for _, entry := range entries {
		if entry.Name() != name && strings.EqualFold(entry.Name(), name) {
			return fmt.Errorf("extension resource %s has conflicting filename case", name)
		}
	}
	return nil
}

func extensionUnchanged(directory string, r *sourceReader) error {
	if err := r.unchanged(); err != nil {
		return err
	}
	current, err := os.Lstat(directory)
	if err != nil || !sameFile(r.stats["."], current) {
		return errors.New("extension directory binding changed during capture")
	}
	return nil
}
