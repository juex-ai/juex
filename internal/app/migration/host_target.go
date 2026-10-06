package migration

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

// HostTarget reads the private operator configuration of an existing Host
// deployment. Database, Blob and maintenance paths come from that deployment,
// not ambient variables or the migration bundle. This never starts services.
func HostTarget(directory string, target BundleTarget) (ApplyConfig, error) {
	var empty ApplyConfig
	root, err := openBundleRoot(directory)
	if err != nil {
		return empty, err
	}
	defer func() { _ = root.Close() }()
	verify, err := hostTargetPaths(root, directory)
	if err != nil {
		return empty, err
	}
	data, err := readBundleFile(root, "deployment.json", 1<<20)
	if err != nil {
		return empty, err
	}
	// The operator owns the full deployment schema. Only its stable identity
	// and path binding fields are consumed here; duplicate JSON keys still fail.
	var raw map[string]json.RawMessage
	if err := decodeBundleJSON(data, &raw); err != nil {
		return empty, err
	}
	var config struct {
		Format    int    `json:"format"`
		Backend   string `json:"backend"`
		Root      string `json:"root"`
		Workspace string `json:"workspace"`
		Identity  string `json:"identity"`
		PublicURL string `json:"public_url"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		return empty, errors.New("invalid Host deployment metadata")
	}
	if config.Format != 2 || config.Backend != "host" || config.Root != directory || !filepath.IsAbs(config.Workspace) || config.Identity == "" || target.DeploymentID != "" && target.DeploymentID != config.Identity {
		return empty, errors.New("host deployment identity or directory does not match")
	}
	data, err = readBundleFile(root, "host.json", 1<<20)
	if err != nil {
		return empty, err
	}
	var host managed.HostConfiguration
	if err := decodeBundleJSON(data, &host); err != nil {
		return empty, err
	}
	if host.Backend.Identity != config.Identity || host.Backend.Root != config.Workspace || host.Backend.ControlRoot != filepath.Join(directory, "control") || host.Backend.Server != config.PublicURL || host.KeyFile != filepath.Join(directory, "secrets/host.key") {
		return empty, errors.New("host execution binding differs from deployment metadata")
	}
	info, err := root.Lstat("secrets")
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return empty, errors.New("host secrets must be a private real directory")
	}
	password, err := readBundleFile(root, "secrets/postgres-password", 1024)
	if err != nil {
		return empty, err
	}
	if value, err := hex.DecodeString(string(password)); err != nil || len(value) != 32 {
		return empty, errors.New("invalid Host database secret")
	}
	encodedKey, err := readBundleFile(root, "secrets/master-key", 1024)
	if err != nil {
		return empty, err
	}
	key, err := hex.DecodeString(string(encodedKey))
	if err != nil || len(key) != 32 {
		return empty, errors.New("invalid Host deployment key")
	}
	address := url.URL{Scheme: "postgres", User: url.UserPassword("juex", string(password)), Path: "/juex", RawQuery: url.Values{"host": {filepath.Join(directory, "socket")}, "port": {"5432"}, "sslmode": {"disable"}}.Encode()}
	target.DeploymentID = config.Identity
	return ApplyConfig{Target: target, MaintenanceDirectory: filepath.Join(directory, "maintenance"), DatabaseURL: address.String(), MasterKey: key, PublicURL: config.PublicURL, BlobDirectory: filepath.Join(directory, "blobs"), BlobCapacity: 20 << 30, verifyTarget: verify}, nil
}

// Keep database, Blob and maintenance bound to the same existing deployment.
// An inherited admission lock alone cannot authorize a partial restore.
func hostTargetPaths(root *os.Root, directory string) (func() error, error) {
	paths := map[string]os.FileInfo{}
	for _, name := range []string{".", "socket", "blobs", "maintenance"} {
		info, err := root.Lstat(name)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("host storage paths must be existing private real directories")
		}
		paths[filepath.Join(directory, name)] = info
	}
	verify := func() error {
		for path, expected := range paths {
			actual, err := os.Lstat(path)
			if err != nil || !actual.IsDir() || actual.Mode().Perm()&0077 != 0 || !os.SameFile(expected, actual) {
				return errors.New("host storage directory binding changed")
			}
		}
		for _, name := range []string{"install-incomplete", "restore-incomplete", "recovery-required.json"} {
			_, err := os.Lstat(filepath.Join(directory, "maintenance", name))
			if !os.IsNotExist(err) {
				return errors.New("host deployment installation or recovery is unfinished")
			}
		}
		return nil
	}
	if err := verify(); err != nil {
		return nil, err
	}
	return verify, nil
}

// GuardSource uses only the already-verified bundle's source identity. The
// operator must separately prove shutdown; acquiring locks is not that proof.
func (b *Bundle) GuardSource() (*legacy.SourceGuard, error) {
	if b == nil {
		return nil, errors.New("verified migration bundle is required")
	}
	ids := make([]string, 0, len(b.source.Agents))
	for _, agent := range b.source.Agents {
		ids = append(ids, agent.Definition.ID)
	}
	return legacy.AcquireSourceGuard(b.source.SourceHome, ids)
}
