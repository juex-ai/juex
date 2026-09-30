package managed

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/hosted"
)

// HostedConfiguration is operator-owned. Neither this file, the engine socket,
// nor the enrollment key is exposed through model tools or Management APIs.
type HostedConfiguration struct {
	Backend         hosted.Config `json:"backend"`
	KeyFile         string        `json:"key_file"`
	IdleSeconds     int           `json:"idle_seconds"`
	Memory          int64         `json:"memory_bytes"`
	NanoCPUs        int64         `json:"nano_cpus"`
	WorkspaceBytes  int64         `json:"workspace_bytes"`
	WorkspaceInodes int64         `json:"workspace_inodes"`
}

type hostedBackend struct{ docker *hosted.Docker }

func hostedSpec(resource execution.HostedResource, credential string) hosted.Spec {
	return hosted.Spec{EnvironmentID: resource.EnvironmentID, AgentID: resource.AgentID, TenantID: resource.TenantID, UserID: resource.UserID, Credential: credential, Slot: resource.Slot, Memory: resource.Memory, NanoCPUs: resource.NanoCPUs, StorageIdentity: resource.StorageIdentity, ProjectID: resource.ProjectID, WorkspaceBytes: resource.WorkspaceBytes, WorkspaceInodes: resource.WorkspaceInodes, Provisioned: resource.Provisioned}
}
func (b hostedBackend) Ensure(ctx context.Context, resource execution.HostedResource, credential string) error {
	_, err := b.docker.Ensure(ctx, hostedSpec(resource, credential))
	return err
}
func (b hostedBackend) Stop(ctx context.Context, resource execution.HostedResource) error {
	return b.docker.Stop(ctx, hostedSpec(resource, ""))
}

func (e *Execution) configureHosted(ctx context.Context, path, listen, caPath string, store execution.HostedRepository) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return errors.New("hosted configuration must be a regular operator-owned file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var config HostedConfiguration
	if err := json.Unmarshal(data, &config); err != nil {
		return err
	}
	if err := separateBlobWorkspace(e.blobDirectory, config.Backend.WorkspaceRoot); err != nil {
		return err
	}
	_, port, err := net.SplitHostPort(listen)
	server, urlErr := url.Parse(config.Backend.Server)
	if err != nil || urlErr != nil || port != strconv.Itoa(int(config.Backend.Control.Port())) || server.Hostname() != "execution" {
		return errors.New("hosted control exception must match the dedicated Execution TLS listener and identity")
	}
	info, err = os.Lstat(config.KeyFile)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return errors.New("Execution enrollment key must be a private regular file")
	}
	key, err := os.ReadFile(config.KeyFile)
	if err != nil || len(key) != 32 {
		return errors.New("Execution enrollment key must contain 32 bytes")
	}
	config.Backend.CA, err = os.ReadFile(caPath)
	if err != nil {
		return err
	}
	if config.IdleSeconds < 0 || config.IdleSeconds > 86400 {
		return errors.New("hosted idle timeout must be between zero and 86400 seconds")
	}
	if config.WorkspaceBytes != 0 && (config.WorkspaceBytes < 16<<20 || config.WorkspaceBytes%512 != 0) || config.WorkspaceInodes != 0 && config.WorkspaceInodes < 64 {
		return errors.New("hosted workspace requires a byte hard limit aligned to 512 bytes and at least 64 inodes")
	}
	backend, err := hosted.New(config.Backend)
	if err != nil {
		return err
	}
	if err := backend.Check(ctx); err != nil {
		_ = backend.Close()
		return err
	}
	e.hosted = backend
	e.Service.Hosted = &execution.HostedManager{Store: store, Authority: e.Service.Authority, Backend: hostedBackend{backend}, Key: key, Idle: time.Duration(config.IdleSeconds) * time.Second, Memory: config.Memory, NanoCPUs: config.NanoCPUs, StorageIdentity: config.Backend.StorageIdentity, WorkspaceBytes: config.WorkspaceBytes, WorkspaceInodes: config.WorkspaceInodes}
	return nil
}

func separateBlobWorkspace(blobRoot, workspaceRoot string) error {
	blobRoot, err := filepath.EvalSymlinks(blobRoot)
	if err != nil {
		return err
	}
	workspaceRoot, err = filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return err
	}
	for _, paths := range [][2]string{{blobRoot, workspaceRoot}, {workspaceRoot, blobRoot}} {
		relative, err := filepath.Rel(paths[0], paths[1])
		if err != nil {
			return err
		}
		if relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("platform blob storage must not overlap hosted Workspace storage")
		}
	}
	return nil
}
