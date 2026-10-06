// Package hostservice owns the native executor's local service lifecycle.
package hostservice

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/juex-ai/juex/internal/foundation/processidentity"
)

type Status struct {
	PID           int       `json:"pid"`
	Fingerprint   string    `json:"fingerprint"`
	EnvironmentID string    `json:"environment_id"`
	State         string    `json:"state"`
	Running       bool      `json:"running"`
	Background    bool      `json:"background"`
	Autostart     bool      `json:"autostart"`
	CleanExit     bool      `json:"clean_exit"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Recorder struct {
	directory string
	status    Status
}

// Record starts only after the native Engine has acquired its journal lock.
func Record(directory, environment string, background bool) (*Recorder, error) {
	identity, err := processidentity.Fingerprint(os.Getpid())
	if err != nil {
		return nil, err
	}
	r := &Recorder{directory: directory, status: Status{PID: os.Getpid(), Fingerprint: identity, EnvironmentID: environment, Background: background}}
	return r, r.Update("starting")
}
func (r *Recorder) Update(state string) error {
	r.status.State = state
	r.status.UpdatedAt = time.Now().UTC()
	return writeJSON(filepath.Join(r.directory, "service-state.json"), r.status)
}

// Finish records the original process result. A stop-marker startup never
// replaces it, so an OS supervisor's later exit code cannot hide failed cleanup.
func (r *Recorder) Finish(result error) error {
	r.status.CleanExit = result == nil
	return r.Update("stopped")
}

func StopRequested(directory string) (bool, error) {
	_, err := readPrivate(filepath.Join(directory, "service-stop"))
	if os.IsNotExist(err) {
		return false, nil
	}
	return err == nil, err
}
func readStatus(directory string) (Status, error) {
	v := Status{State: "stopped"}
	data, err := readPrivate(filepath.Join(directory, "service-state.json"))
	if os.IsNotExist(err) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return v, err
	}
	if v.PID > 1 && v.Fingerprint != "" && v.State != "stopped" {
		identity, err := processidentity.Fingerprint(v.PID)
		v.Running = err == nil && identity == v.Fingerprint
	}
	if !v.Running {
		v.State = "stopped"
	}
	return v, nil
}
func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("executor service files must be private regular files")
	}
	return os.ReadFile(path)
}
func writeJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writePrivate(path, data)
}
func writePrivate(path string, data []byte) error {
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("refusing to replace a non-regular service file")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".executor-service-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
