package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

const metadataFile = "session.json"

type metadata struct {
	Alias string `json:"alias,omitempty"`
}

type History struct {
	Sessions []Info `json:"sessions"`
	Last     *Info  `json:"last"`
}

func SetAlias(dir, alias string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(metadata{Alias: alias}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(dir, metadataFile), data, 0o644)
}

func LoadAlias(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, metadataFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	var m metadata
	if err := json.Unmarshal(data, &m); err != nil {
		return "", err
	}
	return m.Alias, nil
}

func LoadHistory(path string) (History, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return History{Sessions: []Info{}}, nil
		}
		return History{}, err
	}
	var h History
	if err := json.Unmarshal(data, &h); err != nil {
		return History{}, err
	}
	if h.Sessions == nil {
		h.Sessions = []Info{}
	}
	return h, nil
}

func RecordHistory(path string, info Info) error {
	if path == "" {
		return nil
	}
	h, err := LoadHistory(path)
	if err != nil {
		return err
	}
	replaced := false
	for i := range h.Sessions {
		if h.Sessions[i].ID == info.ID {
			h.Sessions[i] = info
			replaced = true
			break
		}
	}
	if !replaced {
		h.Sessions = append(h.Sessions, info)
	}
	infoCopy := info
	h.Last = &infoCopy
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
