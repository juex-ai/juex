package managed

import (
	"encoding/json"
	"errors"
	"os"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/host"
)

type HostConfiguration struct {
	Backend     host.Config `json:"backend"`
	KeyFile     string      `json:"key_file"`
	IdleSeconds int         `json:"idle_seconds"`
}

func (e *Execution) configureHost(path string, store execution.ManagedRepository) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("host configuration must be a private regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var config HostConfiguration
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	if config.IdleSeconds < 0 || config.IdleSeconds > 86400 {
		return errors.New("host idle timeout must be between zero and 86400 seconds")
	}
	for _, root := range []string{config.Backend.Root, config.Backend.ControlRoot} {
		if err := separateBlobWorkspace(e.blobDirectory, root); err != nil {
			return err
		}
	}
	info, err = os.Lstat(config.KeyFile)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("host enrollment key must be a private regular file")
	}
	key, err := os.ReadFile(config.KeyFile)
	if err != nil {
		return err
	}
	if len(key) != 32 {
		return errors.New("host enrollment key must contain 32 bytes")
	}
	backend, err := host.New(config.Backend)
	if err != nil {
		return err
	}
	e.Service.Managed = &execution.ManagedManager{Store: store, Authority: e.Service.Authority, Backend: backend, Key: key, Idle: time.Duration(config.IdleSeconds) * time.Second}
	return nil
}
