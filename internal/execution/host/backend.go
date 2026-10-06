// Package host provisions platform-owned directories and independent native
// executors. It does not provide OS isolation or own external paired devices.
package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/connector"
	"github.com/juex-ai/juex/internal/execution/hostservice"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type Config struct {
	Root         string `json:"root"`
	ControlRoot  string `json:"control_root"`
	Identity     string `json:"identity"`
	Executable   string `json:"executable"`
	Server       string `json:"server"`
	CAFile       string `json:"ca_file"`
	ServerName   string `json:"server_name"`
	InsecureHTTP bool   `json:"insecure_http"`
}

type localService interface {
	Start(context.Context) (hostservice.Status, error)
	Stop(context.Context) error
	Remove(context.Context) error
	Status() (hostservice.Status, error)
}

type Backend struct {
	config  Config
	service func(string) (localService, error)
}

type ownership struct {
	DeploymentID  string `json:"deployment_id"`
	EnvironmentID string `json:"environment_id"`
	AgentID       string `json:"agent_id"`
	TenantID      string `json:"tenant_id"`
	UserID        string `json:"user_id"`
}

func New(config Config) (*Backend, error) {
	return open(config, true)
}

// OpenExisting never adopts or initializes directories during offline work.
func OpenExisting(config Config) (*Backend, error) {
	return open(config, false)
}

func open(config Config, initialize bool) (*Backend, error) {
	if !validID(config.Identity) || runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, execprotocol.ErrInvalid
	}
	u, err := url.Parse(config.Server)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || u.Scheme != "https" && (!config.InsecureHTTP || u.Scheme != "http") {
		return nil, errors.New("host executor requires an HTTPS origin or explicit development HTTP")
	}
	for _, path := range []string{config.Root, config.ControlRoot} {
		if err := privateDirectory(path); err != nil {
			return nil, err
		}
	}
	for _, paths := range [][2]string{{config.Root, config.ControlRoot}, {config.ControlRoot, config.Root}} {
		rel, err := filepath.Rel(paths[0], paths[1])
		if err != nil || rel == "." || filepath.IsLocal(rel) {
			return nil, errors.New("host workspace and control roots must be separate")
		}
	}
	if !filepath.IsAbs(config.Executable) {
		return nil, errors.New("host executor path must be absolute")
	}
	info, err := os.Stat(config.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return nil, errors.New("host executor is not executable")
	}
	for root, role := range map[string]string{config.Root: "workspace", config.ControlRoot: "control"} {
		if err := rootIdentity(root, config.Identity, role, initialize); err != nil {
			return nil, err
		}
	}
	b := &Backend{config: config}
	b.service = func(directory string) (localService, error) { return hostservice.New(directory, config.Executable) }
	return b, nil
}

func validID(value string) bool {
	id, err := uuid.Parse(value)
	return err == nil && id != uuid.Nil && id.String() == value
}

func privateDirectory(path string) error {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if !filepath.IsAbs(path) || canonical != path {
		return errors.New("host directories must be absolute and canonical")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("host directories must be private to the OS user")
	}
	return nil
}

func readPrivate(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return errors.New("host ownership files must be private regular files")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func writeNew(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(data)
	return errors.Join(writeErr, f.Sync(), f.Close())
}

func rootIdentity(root, id, role string, create bool) error {
	if err := privateDirectory(root); err != nil {
		return err
	}
	want := map[string]string{"deployment_id": id, "role": role}
	var got map[string]string
	err := readPrivate(filepath.Join(root, "owner.json"), &got)
	if os.IsNotExist(err) && create {
		entries, readErr := os.ReadDir(root)
		if readErr != nil {
			return readErr
		}
		if len(entries) != 0 {
			return errors.New("refusing to adopt an existing Host directory")
		}
		return writeNew(filepath.Join(root, "owner.json"), want)
	}
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(got, want) {
		return errors.New("host root ownership changed")
	}
	return nil
}

func (b *Backend) Resource(id string) (execution.ManagedResource, error) {
	if !validID(id) {
		return execution.ManagedResource{}, execprotocol.ErrInvalid
	}
	return execution.ManagedResource{EnvironmentID: id, Backend: "host", OS: runtime.GOOS,
		WorkingDirectory: filepath.Join(b.config.Root, id, "workspace"), HomeDirectory: filepath.Join(b.config.Root, id, "home")}, nil
}

func (b *Backend) owner(r execution.ManagedResource) (ownership, error) {
	if !validID(r.AgentID) || !validID(r.TenantID) || !validID(r.UserID) {
		return ownership{}, execprotocol.ErrInvalid
	}
	want, err := b.Resource(r.EnvironmentID)
	if err != nil || r.Backend != want.Backend || r.WorkingDirectory != want.WorkingDirectory || r.HomeDirectory != want.HomeDirectory || r.OS != want.OS {
		return ownership{}, errors.New("host resource location does not match deployment")
	}
	for root, role := range map[string]string{b.config.Root: "workspace", b.config.ControlRoot: "control"} {
		if err := rootIdentity(root, b.config.Identity, role, false); err != nil {
			return ownership{}, err
		}
	}
	return ownership{DeploymentID: b.config.Identity, EnvironmentID: r.EnvironmentID, AgentID: r.AgentID, TenantID: r.TenantID, UserID: r.UserID}, nil
}

func verifyOwned(root string, owner ownership) error {
	path := filepath.Join(root, owner.EnvironmentID)
	if err := privateDirectory(path); err != nil {
		return err
	}
	var got ownership
	if err := readPrivate(filepath.Join(path, "owner.json"), &got); err != nil {
		return fmt.Errorf("host environment ownership is unavailable: %v", err)
	}
	if got != owner {
		return errors.New("host environment ownership changed")
	}
	return nil
}

// Provision publishes complete private directories; a crash before publication
// leaves no half-written enrollment at the stable environment path.
func provision(root string, owner ownership, existing bool, prepare func(string) error) error {
	destination := filepath.Join(root, owner.EnvironmentID)
	if _, err := os.Lstat(destination); err == nil {
		return verifyOwned(root, owner)
	} else if !os.IsNotExist(err) {
		return err
	}
	if existing {
		return errors.New("provisioned Host environment has missing state")
	}
	stage, err := os.MkdirTemp(root, ".provision-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err := writeNew(filepath.Join(stage, "owner.json"), owner); err != nil {
		return err
	}
	if err := prepare(stage); err != nil {
		return err
	}
	if err := os.Rename(stage, destination); err != nil {
		return err
	}
	directory, err := os.Open(root)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func (b *Backend) Ensure(ctx context.Context, r execution.ManagedResource, credential string) error {
	owner, err := b.owner(r)
	if err != nil {
		return err
	}
	if err := provision(b.config.Root, owner, r.Provisioned, func(path string) error {
		for _, name := range []string{"home", "workspace"} {
			if err := os.Mkdir(filepath.Join(path, name), 0700); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	for _, path := range []string{r.WorkingDirectory, r.HomeDirectory} {
		if err := privateDirectory(path); err != nil {
			return err
		}
	}
	grants := map[string][]execprotocol.Capability{r.AgentID: {execprotocol.Files, execprotocol.Shell, execprotocol.MCP}}
	enrollment := connector.Enrollment{Server: b.config.Server, Credential: credential, InsecureHTTP: b.config.InsecureHTTP,
		HomeDirectory: r.HomeDirectory, CAFile: b.config.CAFile, ServerName: b.config.ServerName,
		Device: execution.Device{Environment: execprotocol.Environment{ID: r.EnvironmentID, Kind: "native", OS: r.OS, Name: "Host workspace", WorkingDirectory: r.WorkingDirectory},
			Managed: true, TenantID: r.TenantID, UserID: r.UserID, Status: "active", Version: 1, Grants: grants, Ceiling: grants}}
	if err := provision(b.config.ControlRoot, owner, r.Provisioned, func(path string) error { return writeNew(filepath.Join(path, "enrollment.json"), enrollment) }); err != nil {
		return err
	}
	control := filepath.Join(b.config.ControlRoot, r.EnvironmentID)
	var saved connector.Enrollment
	if err := readPrivate(filepath.Join(control, "enrollment.json"), &saved); err != nil {
		return err
	}
	if !reflect.DeepEqual(saved, enrollment) {
		return errors.New("host enrollment changed; explicit migration is required")
	}
	manager, err := b.service(control)
	if err != nil {
		return err
	}
	status, err := manager.Status()
	if err != nil {
		return err
	}
	if status.Running {
		if status.EnvironmentID != r.EnvironmentID {
			return errors.New("running Host executor identity mismatch")
		}
		return nil
	}
	status, err = manager.Start(ctx)
	if err != nil {
		return err
	}
	if !status.Running || status.EnvironmentID != r.EnvironmentID {
		return errors.New("host executor did not report its expected identity")
	}
	return nil
}

func (b *Backend) Stop(ctx context.Context, r execution.ManagedResource) error {
	owner, err := b.owner(r)
	if err != nil {
		return err
	}
	err = verifyOwned(b.config.ControlRoot, owner)
	if os.IsNotExist(err) && !r.Provisioned {
		return nil
	}
	if err != nil {
		return err
	}
	manager, err := b.service(filepath.Join(b.config.ControlRoot, r.EnvironmentID))
	if err != nil {
		return err
	}
	status, err := manager.Status()
	if err != nil {
		return err
	}
	if status.Running && status.EnvironmentID != r.EnvironmentID {
		return errors.New("host executor identity mismatch")
	}
	return manager.Stop(ctx)
}

func (b *Backend) Purge(ctx context.Context, r execution.ManagedResource) error {
	owner, err := b.owner(r)
	if err != nil {
		return err
	}
	if r.Unconfirmed {
		return errors.New("host operations remain unconfirmed")
	}
	for _, root := range []string{b.config.Root, b.config.ControlRoot} {
		if err := verifyOwned(root, owner); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	control := filepath.Join(b.config.ControlRoot, r.EnvironmentID)
	if _, err := os.Lstat(control); err == nil {
		manager, err := b.service(control)
		if err != nil {
			return err
		}
		status, err := manager.Status()
		if err != nil {
			return err
		}
		if status.Running && status.EnvironmentID != r.EnvironmentID {
			return errors.New("host executor identity mismatch")
		}
		if err := manager.Remove(ctx); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, root := range []string{b.config.Root, b.config.ControlRoot} {
		if err := verifyOwned(root, owner); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		boundary, err := os.OpenRoot(root)
		if err != nil {
			return err
		}
		err = boundary.RemoveAll(r.EnvironmentID)
		closeErr := boundary.Close()
		if err != nil || closeErr != nil {
			return fmt.Errorf("host cleanup: %w", errors.Join(err, closeErr))
		}
	}
	return nil
}

var _ execution.ManagedBackend = (*Backend)(nil)
