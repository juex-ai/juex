//go:build linux

package hosted

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/google/uuid"
)

func TestQuotaKernelLayouts(t *testing.T) {
	if unsafe.Sizeof(projectQuota{}) != 112 || unsafe.Sizeof(projectAttributes{}) != 28 || unsafe.Sizeof(quotaState{}) != 160 {
		t.Fatal("Linux XFS quota ABI mismatch")
	}
}

func TestHostedXFSStorageIdentityAndInheritance(t *testing.T) {
	root, identity := os.Getenv("JUEX_HOSTED_STORAGE_ROOT"), os.Getenv("JUEX_HOSTED_STORAGE_ID")
	if root == "" || identity == "" {
		t.Skip("requires isolated XFS project-quota fixture")
	}
	config := Config{Root: t.TempDir(), WorkspaceRoot: root, StorageIdentity: identity}
	spec := Spec{EnvironmentID: uuid.NewString(), ProjectID: 3900, StorageIdentity: identity, WorkspaceBytes: 32 << 20, WorkspaceInodes: 128}
	ctx := context.Background()
	defer func() { _ = os.RemoveAll(filepath.Join(root, spec.EnvironmentID)) }()
	if err := prepareStorage(ctx, config, spec); err != nil {
		t.Fatal(err)
	}
	spec.Provisioned = true
	if err := prepareStorage(ctx, config, spec); err != nil {
		t.Fatal("restart", err)
	}
	changed := spec
	changed.WorkspaceBytes *= 2
	if err := prepareStorage(ctx, config, changed); err == nil {
		t.Fatal("silently changed persisted hard quota")
	}
	config.WorkspaceRoot = t.TempDir()
	if err := prepareStorage(ctx, config, spec); err == nil {
		t.Fatal("accepted missing storage mount")
	}
	config.WorkspaceRoot = root
	config.StorageIdentity = uuid.NewString()
	if err := prepareStorage(ctx, config, spec); err == nil {
		t.Fatal("accepted wrong storage identity")
	}
	config.StorageIdentity = identity
	if err := os.Remove(filepath.Join(root, spec.EnvironmentID, "home")); err != nil {
		t.Fatal(err)
	}
	if err := prepareStorage(ctx, config, spec); err == nil {
		t.Fatal("silently recreated missing persisted home")
	}
}

func TestHostedXFSRejectsDisabledEnforcement(t *testing.T) {
	root, identity := os.Getenv("JUEX_HOSTED_STORAGE_ROOT"), os.Getenv("JUEX_HOSTED_STORAGE_ID")
	if root == "" || identity == "" {
		t.Skip("requires isolated XFS project-quota fixture")
	}
	config := Config{Root: t.TempDir(), WorkspaceRoot: root, StorageIdentity: identity}
	file, err := storageMount(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	flags := uint32(0x20)
	if err := quotaCall(int(file.Fd()), 2, 0, unsafe.Pointer(&flags)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := quotaCall(int(file.Fd()), 1, 0, unsafe.Pointer(&flags)); err != nil {
			t.Error("restore fixture enforcement", err)
		}
	}()
	opened, err := storageMount(context.Background(), config)
	if err == nil {
		_ = opened.Close()
		t.Fatal("accepted disabled quota enforcement")
	}
}

func TestHostedXFSPurgeChecksOwnershipAndSurvivesRetry(t *testing.T) {
	root, identity := os.Getenv("JUEX_HOSTED_STORAGE_ROOT"), os.Getenv("JUEX_HOSTED_STORAGE_ID")
	if root == "" || identity == "" {
		t.Skip("requires isolated XFS project-quota fixture")
	}
	config := Config{Root: t.TempDir(), WorkspaceRoot: root, StorageIdentity: identity}
	spec := Spec{EnvironmentID: uuid.NewString(), ProjectID: 3901, StorageIdentity: identity, WorkspaceBytes: 32 << 20, WorkspaceInodes: 128}
	ctx := context.Background()
	defer func() { _ = os.RemoveAll(filepath.Join(root, spec.EnvironmentID)) }()
	if err := prepareStorage(ctx, config, spec); err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(root, spec.EnvironmentID)
	file := filepath.Join(workspace, "workspace", "private-data")
	if err := os.WriteFile(file, []byte("private workspace"), 0600); err != nil {
		t.Fatal(err)
	}
	wrong := spec
	wrong.ProjectID++
	if err := purgeStorage(ctx, config, wrong); err == nil {
		t.Fatal("purge accepted a different allocation")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("failed purge changed workspace", err)
	}
	if err := purgeStorage(ctx, config, spec); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{workspace, filepath.Join(config.Root, spec.EnvironmentID)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("purge retained owned data", path, err)
		}
	}
	if err := purgeStorage(ctx, config, spec); err != nil {
		t.Fatal("lost cleanup reply cannot be retried", err)
	}
}
