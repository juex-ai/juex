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
	"unsafe"

	"golang.org/x/sys/unix"
)

// These fixed-width layouts are the Linux UAPI in linux/dqblk_xfs.h and
// linux/fs.h. Quotas use 512-byte blocks, independently of filesystem blocks.
type projectQuota struct {
	Version, Flags                                             int8
	Mask                                                       uint16
	ID                                                         uint32
	BlockHard, BlockSoft, InodeHard, InodeSoft, Blocks, Inodes uint64
	InodeTimer, BlockTimer                                     int32
	InodeWarnings, BlockWarnings                               uint16
	TimerHigh                                                  [4]byte
	RealtimeHard, RealtimeSoft, RealtimeBlocks                 uint64
	RealtimeTimer                                              int32
	RealtimeWarnings                                           uint16
	Padding                                                    [10]byte
}
type quotaState struct {
	Version, Padding uint8
	Flags            uint16
	Count            uint32
	Files            [9]uint64
	Timers           [3]int32
	Warnings         [3]uint16
	Padding2         uint16
	Padding3         uint32
	Reserved         [7]uint64
}
type projectAttributes struct {
	Flags, ExtentSize, Extents, ProjectID, CowExtentSize uint32
	Padding                                              [8]byte
}

func quotaCall(fd int, command, id uintptr, data unsafe.Pointer) error {
	_, _, errno := unix.Syscall6(unix.SYS_QUOTACTL_FD, uintptr(fd), ((('X'<<8)+command)<<8)|2, id, uintptr(data), 0, 0)
	if errno != 0 {
		return fmt.Errorf("XFS project quota: %w", errno)
	}
	return nil
}

func storageMount(ctx context.Context, config Config) (*os.File, error) {
	if !filepath.IsAbs(config.WorkspaceRoot) || config.StorageIdentity == "" {
		return nil, errors.New("hosted storage requires a dedicated XFS mount and its UUID")
	}
	info, err := os.Lstat(config.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || !controlOwned(info) || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("hosted storage mount must be private and owned by root")
	}
	output, err := exec.CommandContext(ctx, "findmnt", "--json", "--mountpoint", config.WorkspaceRoot, "--output", "UUID,FSTYPE,TARGET").Output()
	if err != nil {
		return nil, fmt.Errorf("find hosted storage mount: %w", err)
	}
	var mounts struct {
		Filesystems []struct{ UUID, Fstype, Target string }
	}
	if err := json.Unmarshal(output, &mounts); err != nil {
		return nil, err
	}
	if len(mounts.Filesystems) != 1 || mounts.Filesystems[0].UUID != config.StorageIdentity || mounts.Filesystems[0].Fstype != "xfs" || mounts.Filesystems[0].Target != filepath.Clean(config.WorkspaceRoot) {
		return nil, errors.New("hosted storage mount identity changed or is not XFS")
	}
	fd, err := unix.Open(config.WorkspaceRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), config.WorkspaceRoot)
	var storageInfo, controlInfo unix.Stat_t
	if err := unix.Fstat(fd, &storageInfo); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := unix.Stat(config.Root, &controlInfo); err != nil {
		_ = file.Close()
		return nil, err
	}
	if storageInfo.Dev == controlInfo.Dev {
		_ = file.Close()
		return nil, errors.New("hosted workspaces must use a filesystem separate from control state")
	}
	state := quotaState{Version: 1}
	err = quotaCall(fd, 8, 0, unsafe.Pointer(&state))
	if err == nil && state.Flags&0x30 != 0x30 {
		err = errors.New("XFS project quota accounting and enforcement must both be enabled")
	}
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func projectDirectory(path string, id uint32, create bool, uid int) error {
	if create {
		if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
			return err
		}
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	var attributes projectAttributes
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0x801c581f, uintptr(unsafe.Pointer(&attributes)))
	if errno != 0 {
		return errno
	}
	if create && attributes.ProjectID == 0 {
		var info unix.Stat_t
		if err := unix.Fstat(fd, &info); err != nil {
			return err
		}
		if info.Uid != 0 {
			return errors.New("unallocated hosted directory is not owned by root")
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		if len(entries) != 0 {
			return errors.New("refusing to assign quota to an existing unaccounted tree")
		}
		attributes.ProjectID, attributes.Flags = id, attributes.Flags|0x200
		_, _, errno = unix.Syscall(unix.SYS_IOCTL, uintptr(fd), 0x401c5820, uintptr(unsafe.Pointer(&attributes)))
		if errno != 0 {
			return errno
		}
	}
	if attributes.ProjectID != id || attributes.Flags&0x200 == 0 {
		return errors.New("hosted directory quota identity or inheritance changed")
	}
	if create {
		return unix.Fchown(fd, uid, uid)
	}
	return nil
}

func prepareStorage(ctx context.Context, config Config, spec Spec) error {
	if spec.StorageIdentity != config.StorageIdentity || spec.ProjectID == 0 || spec.WorkspaceBytes < 16<<20 || spec.WorkspaceBytes%512 != 0 || spec.WorkspaceInodes < 64 {
		return errors.New("invalid persisted hosted storage allocation")
	}
	pool, err := storageMount(ctx, config)
	if err != nil {
		return err
	}
	defer func() { _ = pool.Close() }()
	control := filepath.Join(config.Root, spec.EnvironmentID)
	if err := privateDirectory(control, 0700, 0); err != nil {
		return err
	}
	marker := filepath.Join(control, "storage.json")
	wanted, _ := json.Marshal(struct {
		Identity string
		Project  uint32
	}{spec.StorageIdentity, spec.ProjectID})
	existing, err := os.ReadFile(marker)
	create := os.IsNotExist(err) && !spec.Provisioned
	if !create && (err != nil || string(existing) != string(wanted)) {
		return errors.New("hosted storage allocation marker missing or changed")
	}
	quota := projectQuota{Version: 1, Flags: 2, ID: spec.ProjectID}
	err = quotaCall(int(pool.Fd()), 3, uintptr(spec.ProjectID), unsafe.Pointer(&quota))
	if err != nil && (!create || !errors.Is(err, unix.ENOENT)) {
		return err
	}
	if create {
		quota.Version, quota.Flags, quota.Mask, quota.ID = 1, 2, 15, spec.ProjectID
		quota.BlockHard, quota.BlockSoft, quota.InodeHard, quota.InodeSoft = uint64(spec.WorkspaceBytes/512), 0, uint64(spec.WorkspaceInodes), 0
		if err := quotaCall(int(pool.Fd()), 4, uintptr(spec.ProjectID), unsafe.Pointer(&quota)); err != nil {
			return err
		}
	} else if quota.BlockHard != uint64(spec.WorkspaceBytes/512) || quota.InodeHard != uint64(spec.WorkspaceInodes) {
		return errors.New("hosted hard quota changed")
	}
	root := filepath.Join(config.WorkspaceRoot, spec.EnvironmentID)
	if !create {
		if _, err := os.Lstat(root); err != nil {
			return err
		}
	}
	if err := privateDirectory(root, 0700, 0); err != nil {
		return err
	}
	for _, name := range []string{"workspace", "home"} {
		if err := projectDirectory(filepath.Join(root, name), spec.ProjectID, create, 1000); err != nil {
			return err
		}
	}
	if create {
		return privateFile(marker, wanted)
	}
	return nil
}

func purgeStorage(ctx context.Context, config Config, spec Spec) error {
	if spec.StorageIdentity != config.StorageIdentity || spec.ProjectID == 0 {
		return errors.New("hosted purge storage identity mismatch")
	}
	pool, err := storageMount(ctx, config)
	if err != nil {
		return err
	}
	defer func() { _ = pool.Close() }()
	control := filepath.Join(config.Root, spec.EnvironmentID)
	root := filepath.Join(config.WorkspaceRoot, spec.EnvironmentID)
	rootInfo, rootErr := os.Lstat(root)
	controlInfo, controlErr := os.Lstat(control)
	if os.IsNotExist(rootErr) && os.IsNotExist(controlErr) {
		return nil
	}
	if rootErr != nil && !os.IsNotExist(rootErr) {
		return rootErr
	}
	if controlErr != nil && !os.IsNotExist(controlErr) {
		return controlErr
	}
	if rootErr == nil && (!rootInfo.IsDir() || !controlOwned(rootInfo) || rootInfo.Mode().Perm()&0077 != 0) {
		return errors.New("hosted purge workspace ownership mismatch")
	}
	if controlErr == nil && (!controlInfo.IsDir() || !controlOwned(controlInfo) || controlInfo.Mode().Perm()&0077 != 0) {
		return errors.New("hosted purge control ownership mismatch")
	}
	wanted, _ := json.Marshal(struct {
		Identity string
		Project  uint32
	}{spec.StorageIdentity, spec.ProjectID})
	marker, err := os.ReadFile(filepath.Join(control, "storage.json"))
	if err == nil && string(marker) != string(wanted) {
		return errors.New("hosted purge allocation marker mismatch")
	}
	if err != nil && (!os.IsNotExist(err) || spec.Provisioned && rootErr == nil) {
		return errors.New("hosted purge allocation marker missing")
	}
	if rootErr == nil {
		entries, err := os.ReadDir(root)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != "workspace" && entry.Name() != "home" {
				return errors.New("unexpected hosted storage entry")
			}
			if err = projectDirectory(filepath.Join(root, entry.Name()), spec.ProjectID, false, 1000); err != nil {
				return err
			}
		}
		if err = os.RemoveAll(root); err != nil {
			return err
		}
	}
	// Keep the marker until data is gone so crash retries verify the allocation.
	return os.RemoveAll(control)
}
