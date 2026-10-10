package native

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/juex-ai/juex/internal/foundation/extensionpolicy"
	"gopkg.in/yaml.v3"
)

// Skill inspection is an explicit source operation. It reads only SKILL.md
// entrypoints; scripts, dotfiles and parent/user directories are never scanned.
func inspectSkills(ctx context.Context, directory string, writer io.Writer) error {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	paths := []string{}
	skipped := []extensionpolicy.SkippedSource{}
	if info, err := root.Stat("SKILL.md"); err == nil {
		if !info.Mode().IsRegular() {
			return extensionpolicy.ErrInvalid
		}
		paths = append(paths, "SKILL.md")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	} else {
		dir, err := root.Open(".")
		if err != nil {
			return err
		}
		entries, err := dir.ReadDir(1025)
		_ = dir.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		if len(entries) > 1024 {
			return errors.New("skill directory has too many entries; select a narrower source")
		}
		for _, entry := range entries {
			if entry.Type()&os.ModeSymlink != 0 {
				target, err := root.Readlink(entry.Name())
				if err != nil {
					return err
				}
				if !filepath.IsAbs(target) {
					target = filepath.Clean(filepath.Join(directory, target))
				}
				skipped = append(skipped, extensionpolicy.SkippedSource{Path: entry.Name(), Reason: "symbolic link: inspect its target as an explicit source", Target: target})
				continue
			}
			if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}
			name := entry.Name() + "/SKILL.md"
			if _, err := root.Stat(name); errors.Is(err, fs.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			paths = append(paths, name)
		}
	}
	slices.Sort(paths)
	if len(paths) > 256 || len(paths) == 0 && len(skipped) == 0 {
		return errors.New("select a source containing up to 256 skill entrypoints")
	}
	catalog := extensionpolicy.Catalog{SourceKind: "skills", Skipped: skipped, Manifest: extensionpolicy.Manifest{ManifestVersion: 2, Name: "skills", Version: "snapshot", Skills: []extensionpolicy.SkillResource{}}, Skills: []extensionpolicy.SkillContent{}}
	for _, name := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := extensionFile(root, name, 64<<10)
		if err != nil {
			return err
		}
		id := filepath.Base(directory)
		if name != "SKILL.md" {
			id = strings.Split(name, "/")[0]
		}
		id, description, err := skillMetadata(data, id)
		if err != nil {
			return err
		}
		catalog.Manifest.Skills = append(catalog.Manifest.Skills, extensionpolicy.SkillResource{ID: id, Description: description, Path: name})
		catalog.Skills = append(catalog.Skills, extensionpolicy.SkillContent{ID: id, Content: string(data)})
	}
	catalog.Revision = catalog.Digest()
	if err := catalog.Validate(); err != nil {
		return err
	}
	return json.NewEncoder(writer).Encode(catalog)
}

func skillMetadata(data []byte, fallback string) (string, string, error) {
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return fallback, "", nil
	}
	lines := strings.Split(text, "\n")
	end := 0
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end == 0 {
		return "", "", errors.New("skill frontmatter has no closing fence")
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")+"\n"), &metadata); err != nil {
		return "", "", err
	}
	if metadata.Name == "" {
		metadata.Name = fallback
	}
	return metadata.Name, metadata.Description, nil
}
