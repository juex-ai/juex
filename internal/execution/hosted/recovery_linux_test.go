//go:build linux

package hosted

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/google/uuid"
)

func TestHostedXFSRecoveryPreservesPartialAllocationAndQuota(t *testing.T) {
	oldRoot, oldID := os.Getenv("JUEX_HOSTED_STORAGE_ROOT"), os.Getenv("JUEX_HOSTED_STORAGE_ID")
	newRoot, newID := os.Getenv("JUEX_HOSTED_RECOVERY_STORAGE_ROOT"), os.Getenv("JUEX_HOSTED_RECOVERY_STORAGE_ID")
	if oldRoot == "" || oldID == "" || newRoot == "" || newID == "" {
		t.Skip("requires two isolated XFS project-quota fixtures")
	}
	for _, provisioned := range []bool{false, true} {
		t.Run(map[bool]string{false: "allocated_before_container_failure", true: "provisioned"}[provisioned], func(t *testing.T) {
			ctx := context.Background()
			source := Config{Root: t.TempDir(), WorkspaceRoot: oldRoot, StorageIdentity: oldID}
			target := Config{Root: t.TempDir(), WorkspaceRoot: newRoot, StorageIdentity: newID}
			spec := Spec{EnvironmentID: uuid.NewString(), ProjectID: 3911, StorageIdentity: oldID, WorkspaceBytes: 32 << 20, WorkspaceInodes: 128}
			t.Cleanup(func() {
				for _, root := range []string{oldRoot, newRoot} {
					if err := os.RemoveAll(filepath.Join(root, spec.EnvironmentID)); err != nil {
						t.Error(err)
					}
				}
			})
			if err := prepareStorage(ctx, source, spec); err != nil {
				t.Fatal(err)
			}
			spec.Provisioned = provisioned
			from := filepath.Join(oldRoot, spec.EnvironmentID)
			if err := os.WriteFile(filepath.Join(from, "workspace", "data"), []byte("recover me"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(from, "home", ".profile"), []byte("home data"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("data", filepath.Join(from, "workspace", "link")); err != nil {
				t.Fatal(err)
			}
			marker, err := os.ReadFile(filepath.Join(source.Root, spec.EnvironmentID, "storage.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(filepath.Join(target.Root, spec.EnvironmentID), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(target.Root, spec.EnvironmentID, "storage.json"), marker, 0600); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "workspaces.tar")
			if output, err := exec.Command("tar", "-cpf", archive, "-C", oldRoot, spec.EnvironmentID).CombinedOutput(); err != nil {
				t.Fatal(err, string(output))
			}
			if err := RecoverStorage(ctx, target, spec, oldID, true); err != nil {
				t.Fatal("initialize", err)
			}
			if output, err := exec.Command("tar", "-xpf", archive, "-C", newRoot).CombinedOutput(); err != nil {
				t.Fatal(err, string(output))
			}
			if err := RecoverStorage(ctx, target, spec, oldID, false); err != nil {
				t.Fatal("validate", err)
			}
			spec.StorageIdentity = newID
			if err := prepareStorage(ctx, target, spec); err != nil {
				t.Fatal("first post-restore startup", err)
			}
			got, err := os.ReadFile(filepath.Join(newRoot, spec.EnvironmentID, "workspace", "link"))
			if err != nil || string(got) != "recover me" {
				t.Fatal("restored link/data", string(got), err)
			}
			pool, err := storageMount(ctx, target)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			quota := projectQuota{Version: 1, Flags: 2, ID: spec.ProjectID}
			if err := quotaCall(int(pool.Fd()), 3, uintptr(spec.ProjectID), unsafe.Pointer(&quota)); err != nil {
				t.Fatal(err)
			}
			if quota.BlockHard != uint64(spec.WorkspaceBytes/512) || quota.InodeHard != uint64(spec.WorkspaceInodes) || quota.Inodes < 5 {
				t.Fatal("restored quota lost accounting or limits", quota)
			}
		})
	}
}
