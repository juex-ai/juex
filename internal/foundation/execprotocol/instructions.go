package execprotocol

import (
	"encoding/json"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

const MaxInstructionFileBytes = 64 << 10
const MaxInstructionSnapshotBytes = 256 << 10

type InstructionArguments struct {
	WorkingDirectory string `json:"working_directory"`
	Path             string `json:"path,omitempty"`
}

type InstructionSource struct {
	Path   string `json:"path"`
	State  string `json:"state"`
	Text   string `json:"text"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type InstructionSnapshot struct {
	Sources []InstructionSource `json:"sources"`
}

func InstructionPaths(directory, global string) ([]string, error) {
	valid := func(value string) bool {
		return path.IsAbs(value) && len(value) <= 4096 && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
	}
	if !valid(directory) || global != "" && !valid(global) {
		return nil, ErrInvalid
	}
	var paths []string
	if global != "" {
		paths = append(paths, path.Clean(global))
	}
	for _, name := range []string{path.Join(directory, "AGENTS.md"), path.Join(directory, ".agents", "AGENTS.md")} {
		if !valid(name) {
			return nil, ErrInvalid
		}
		if !slices.Contains(paths, name) {
			paths = append(paths, name)
		}
	}
	return paths, nil
}

func (s InstructionSnapshot) ValidateSources(directory, global string) error {
	paths, err := InstructionPaths(directory, global)
	if err != nil || s.Validate() != nil || len(paths) != len(s.Sources) {
		return ErrInvalid
	}
	for i, name := range paths {
		if s.Sources[i].Path != name {
			return ErrInvalid
		}
	}
	return nil
}

func (s InstructionSnapshot) Validate() error {
	if len(s.Sources) < 2 || len(s.Sources) > 3 {
		return ErrInvalid
	}
	seen := make(map[string]bool)
	for _, source := range s.Sources {
		if !path.IsAbs(source.Path) || len(source.Path) > 4096 || !utf8.ValidString(source.Path) || strings.ContainsRune(source.Path, 0) || seen[path.Clean(source.Path)] || !utf8.ValidString(source.Text) || strings.ContainsRune(source.Text, 0) || source.Size != int64(len(source.Text)) || source.Size > MaxInstructionFileBytes {
			return ErrInvalid
		}
		seen[path.Clean(source.Path)] = true
		if source.State == "missing" {
			if source.Text != "" || source.SHA256 != "" {
				return ErrInvalid
			}
			continue
		}
		if source.State != "loaded" && source.State != "empty" || (source.State == "empty") != (strings.TrimSpace(source.Text) == "") || source.SHA256 != FileDigest([]byte(source.Text)) {
			return ErrInvalid
		}
	}
	encoded, err := json.Marshal(s)
	if err != nil || len(encoded)+1 > MaxInstructionSnapshotBytes {
		return ErrInvalid
	}
	return nil
}
