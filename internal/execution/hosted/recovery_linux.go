//go:build linux

package hosted

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"
)

// RecoverStorage restores inode project IDs and limits on an already restored,
// offline dedicated filesystem. It never formats disks or deletes user data.
func RecoverStorage(ctx context.Context, config Config, spec Spec, previous string, initialize bool) error {
	marker := filepath.Join(config.Root, spec.EnvironmentID, "storage.json")
	root := filepath.Join(config.WorkspaceRoot, spec.EnvironmentID)
	if !spec.Provisioned {
		_, markerErr := os.Lstat(marker)
		if os.IsNotExist(markerErr) {
			if _, rootErr := os.Lstat(root); os.IsNotExist(rootErr) {
				return nil
			}
			return errors.New("unprovisioned workspace has no allocation marker")
		}
		if markerErr != nil {
			return markerErr
		}
	}
	if spec.StorageIdentity != previous && spec.StorageIdentity != config.StorageIdentity {
		return errors.New("recovery allocation belongs to another storage pool")
	}
	pool, err := storageMount(ctx, config)
	if err != nil {
		return err
	}
	defer func() { _ = pool.Close() }()
	if initialize {
		if err := os.Mkdir(root, 0700); err != nil && !os.IsExist(err) {
			return err
		}
	}
	info, err := os.Lstat(root)
	if err != nil {
		return err
	}
	if !info.IsDir() || !controlOwned(info) || info.Mode().Perm()&0077 != 0 {
		return errors.New("restored workspace root is not private")
	}
	data, err := os.ReadFile(marker)
	if err != nil {
		return err
	}
	var saved struct {
		Identity string
		Project  uint32
	}
	if json.Unmarshal(data, &saved) != nil || saved.Project != spec.ProjectID || (saved.Identity != previous && saved.Identity != config.StorageIdentity) {
		return errors.New("restored allocation marker does not match database")
	}
	for _, name := range []string{"workspace", "home"} {
		path := filepath.Join(root, name)
		if initialize {
			if err := projectDirectory(path, spec.ProjectID, true, 1000); err != nil {
				return err
			}
			continue
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return errors.New("restored workspace/home must be real directories")
		}
		// xfs_quota's command parser does not implement shell quoting. A
		// private projects file keeps paths (including spaces) out of -c.
		if strings.ContainsAny(path, "\n\r:") {
			return errors.New("unsupported project mapping path")
		}
		mapping, err := os.CreateTemp(config.Root, ".recovery-project-*")
		if err != nil {
			return err
		}
		mappingName := mapping.Name()
		defer func() { _ = os.Remove(mappingName) }()
		_, err = fmt.Fprintf(mapping, "%d:%s\n", spec.ProjectID, path)
		err = errors.Join(err, mapping.Close())
		if err != nil {
			return err
		}
		for _, action := range []string{"-c"} {
			command := "project " + action + " " + strconv.FormatUint(uint64(spec.ProjectID), 10)
			var stderr strings.Builder
			cmd := exec.CommandContext(ctx, "xfs_quota", "-x", "-D", mappingName, "-c", command, config.WorkspaceRoot)
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			// xfs_quota can exit zero after reporting a failed path/check.
			// Successful recursive checks print only the summary line.
			if err != nil || recoveryDiagnosticFailed(stderr.String()) || (action == "-c" && recoveryCheckFailed(string(output))) {
				return fmt.Errorf("restore inode project IDs: %v: %s%s", err, stderr.String(), output)
			}
		}
		if err := projectDirectory(path, spec.ProjectID, false, 1000); err != nil {
			return err
		}
	}
	quota := projectQuota{Version: 1, Flags: 2, Mask: 15, ID: spec.ProjectID, BlockHard: uint64(spec.WorkspaceBytes / 512), InodeHard: uint64(spec.WorkspaceInodes)}
	if err := quotaCall(int(pool.Fd()), 4, uintptr(spec.ProjectID), unsafe.Pointer(&quota)); err != nil {
		return err
	}
	saved.Identity = config.StorageIdentity
	data, err = json.Marshal(saved)
	if err != nil {
		return err
	}
	temporary := marker + ".recovery"
	f, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(temporary, marker); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(marker))
	if err != nil {
		return err
	}
	err = directory.Sync()
	_ = directory.Close()
	if err != nil {
		return err
	}
	spec.StorageIdentity = config.StorageIdentity
	return prepareStorage(ctx, config, spec)
}

func recoveryCheckFailed(output string) bool {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line != "" && !strings.HasPrefix(line, "Processed 1 ") && !strings.HasPrefix(line, "Checking project ") {
			return true
		}
	}
	return false
}

func recoveryDiagnosticFailed(output string) bool {
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		// xfs_quota skips symlinks and other non-regular inodes. Their project
		// identity is inherited because extraction follows quota initialization.
		if line != "" && !strings.Contains(line, ": skipping special file ") {
			return true
		}
	}
	return false
}
