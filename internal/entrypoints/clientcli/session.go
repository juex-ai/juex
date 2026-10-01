package clientcli

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/juex-ai/juex/internal/management"
)

type loginSession struct {
	Origin    string          `json:"origin"`
	User      management.User `json:"user"`
	Token     string          `json:"token"`
	ExpiresAt time.Time       `json:"expires_at"`
	TenantID  string          `json:"tenant_id,omitempty"`
}

func defaultSessionFile(origin string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	id := sha256.Sum256([]byte(origin))
	return filepath.Join(dir, "juex", "client", fmt.Sprintf("%x.json", id[:16])), nil
}

func readSession(path, origin string) (loginSession, error) {
	var session loginSession
	if !filepath.IsAbs(path) {
		return session, errors.New("--session-file must be absolute")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return session, nil
	}
	if err != nil {
		return session, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 16<<10 {
		return session, errors.New("session file must be private (0600), regular, and bounded")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return session, err
	}
	if err := json.Unmarshal(data, &session); err != nil {
		return session, errors.New("invalid session file")
	}
	if session.Origin != origin {
		return loginSession{}, errors.New("session belongs to another server; select a different --session-file")
	}
	return session, nil
}

func (c *client) saveSession() error {
	dir := filepath.Dir(c.sessionFile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("session directory must be private (0700)")
	}
	if _, err := readSession(c.sessionFile, c.origin); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".session-*")
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(c.session); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), c.sessionFile)
}
