package native

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func patchFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range map[string]string{"update.txt": "one\ntwo\nthree\n", "delete.txt": "delete\n", "move.txt": "move\n"} {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0750); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestPatchAddUpdateDeleteAndMove(t *testing.T) {
	dir := patchFixture(t)
	patch := "*** Begin Patch\n*** Add File: nested/new.txt\n+你好\n+world\n*** Update File: update.txt\n@@\n one\n-two\n+second\n three\n*** Delete File: delete.txt\n*** Update File: move.txt\n*** Move to: nested/moved.txt\n*** End Patch"
	var output strings.Builder
	if err := RunFileOperation(context.Background(), dir, "apply_patch", FileArguments{PatchText: patch}, &output); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{"nested/new.txt": "你好\nworld\n", "update.txt": "one\nsecond\nthree\n", "nested/moved.txt": "move\n"} {
		data, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil || string(data) != want {
			t.Fatal(path, string(data), err)
		}
	}
	for _, path := range []string{"delete.txt", "move.txt"} {
		if _, err := os.Stat(filepath.Join(dir, path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatal(path, err)
		}
	}
	for _, path := range []string{"update.txt", "nested/moved.txt"} {
		info, err := os.Stat(filepath.Join(dir, path))
		if err != nil || info.Mode().Perm() != 0750 {
			t.Fatal(path, info, err)
		}
	}
	if !strings.Contains(output.String(), "move move.txt -> nested/moved.txt") || !strings.Contains(output.String(), "+1 -1") {
		t.Fatal(output.String())
	}
}

func TestPatchPreflightLeavesEarlierFilesUntouched(t *testing.T) {
	for name, tail := range map[string]string{
		"missing":         "*** Update File: missing.txt\n-old\n+new\n",
		"missing-context": "*** Update File: update.txt\n-absent\n+new\n",
		"existing-add":    "*** Add File: update.txt\n+replacement\n",
		"duplicate":       "*** Delete File: ./update.txt\n",
		"escape":          "*** Add File: ../outside.txt\n+escape\n",
		"directory":       "*** Delete File: .\n",
		"empty-path":      "*** Add File: \n+bad\n",
		"invalid-hunk":    "*** Update File: move.txt\n+insert-without-context\n",
	} {
		t.Run(name, func(t *testing.T) {
			dir := patchFixture(t)
			patch := "*** Begin Patch\n*** Update File: update.txt\n-two\n+changed\n" + tail + "*** End Patch"
			if err := runPatch(context.Background(), dir, patch, io.Discard); err == nil {
				t.Fatal("invalid patch accepted")
			}
			data, _ := os.ReadFile(filepath.Join(dir, "update.txt"))
			if string(data) != "one\ntwo\nthree\n" {
				t.Fatal("preflight changed earlier file", string(data))
			}
		})
	}
}

func TestPatchCanonicalPathsAndExactHunks(t *testing.T) {
	dir := patchFixture(t)
	if err := os.Symlink(dir, filepath.Join(dir, "inside")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "update.txt"), "inside/update.txt"} {
		patch := "*** Begin Patch\n*** Update File: update.txt\n-two\n+changed\n*** Delete File: " + path + "\n*** End Patch"
		if err := runPatch(context.Background(), dir, patch, io.Discard); err == nil {
			t.Fatal("duplicate identity accepted", path)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "outside")); err != nil {
		t.Fatal(err)
	}
	if err := runPatch(context.Background(), dir, "*** Begin Patch\n*** Add File: outside/escape\n+bad\n*** End Patch", io.Discard); err == nil {
		t.Fatal("outside symlink accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "repeated"), []byte("match\nmatch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runPatch(context.Background(), dir, "*** Begin Patch\n*** Update File: repeated\n-match\n+new\n*** End Patch", io.Discard); err == nil {
		t.Fatal("ambiguous hunk accepted")
	}
	patch := "*** Begin Patch\r\n*** Update File: " + filepath.Join(dir, "update.txt") + "\r\n-two\r\n+二\r\n*** End Patch\r\n"
	if err := runPatch(context.Background(), dir, patch, io.Discard); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "update.txt"))
	if string(data) != "one\n二\nthree\n" {
		t.Fatal(string(data))
	}
}

type patchContext struct {
	context.Context
	check func() error
}

func (c patchContext) Err() error { return c.check() }

func TestPatchRollsBackOnlyItsOwnChanges(t *testing.T) {
	for _, concurrent := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "external-edit"}[concurrent], func(t *testing.T) {
			dir := patchFixture(t)
			path := filepath.Join(dir, "update.txt")
			ctx := patchContext{Context: context.Background(), check: func() error {
				data, _ := os.ReadFile(path)
				if string(data) == "one\nchanged\nthree\n" {
					if concurrent {
						if err := os.WriteFile(path, []byte("external edit\n"), 0750); err != nil {
							t.Fatal(err)
						}
					}
					return context.Canceled
				}
				return nil
			}}
			patch := "*** Begin Patch\n*** Update File: update.txt\n-two\n+changed\n*** Delete File: delete.txt\n*** End Patch"
			err := runPatch(ctx, dir, patch, io.Discard)
			if !errors.Is(err, context.Canceled) || errors.Is(err, execprotocol.ErrOutcomeUnknown) != concurrent {
				t.Fatal(err)
			}
			want := "one\ntwo\nthree\n"
			if concurrent {
				want = "external edit\n"
			}
			data, _ := os.ReadFile(path)
			if string(data) != want {
				t.Fatal("rollback overwrote wrong content", string(data))
			}
			data, _ = os.ReadFile(filepath.Join(dir, "delete.txt"))
			if string(data) != "delete\n" {
				t.Fatal("cancel did not stop later delete")
			}
		})
	}
}

func TestPatchParentReplacementDoesNotEscape(t *testing.T) {
	dir := patchFixture(t)
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	if err := os.Mkdir(filepath.Join(dir, "target"), 0755); err != nil {
		t.Fatal(err)
	}
	ops, err := parsePatch("*** Begin Patch\n*** Add File: target/new\n+safe\n*** End Patch")
	if err != nil {
		t.Fatal(err)
	}
	changes, _, err := planNativePatch(context.Background(), root, dir, ops)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "target"), filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "target")); err != nil {
		t.Fatal(err)
	}
	if err := applyNativePatch(context.Background(), root, changes); err == nil {
		t.Fatal("redirected parent accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("wrote outside bound directory", err)
	}
}

type failedPatchOutput struct{}

func (failedPatchOutput) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestPatchLostAcknowledgementIsUnknown(t *testing.T) {
	dir := t.TempDir()
	err := runPatch(context.Background(), dir, "*** Begin Patch\n*** Add File: new\n+written\n*** End Patch", failedPatchOutput{})
	if !errors.Is(err, execprotocol.ErrOutcomeUnknown) {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "new"))
	if err != nil || string(data) != "written\n" {
		t.Fatal(string(data), err)
	}
}

func TestPatchRejectsFileSymlinksWithoutChangingEitherEntry(t *testing.T) {
	for _, op := range []string{"*** Delete File: alias\n", "*** Update File: alias\n*** Move to: moved\n", "*** Update File: alias\n-old\n+new\n"} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "real"), []byte("old\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("real", filepath.Join(dir, "alias")); err != nil {
			t.Fatal(err)
		}
		if err := runPatch(context.Background(), dir, "*** Begin Patch\n"+op+"*** End Patch", io.Discard); err == nil {
			t.Fatal("file symlink accepted")
		}
		data, err := os.ReadFile(filepath.Join(dir, "real"))
		if err != nil || string(data) != "old\n" {
			t.Fatal(string(data), err)
		}
		if target, err := os.Readlink(filepath.Join(dir, "alias")); err != nil || target != "real" {
			t.Fatal(target, err)
		}
	}
}

func TestPatchUsesActualVolumeCaseIdentity(t *testing.T) {
	dir := patchFixture(t)
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	fold, err := patchCaseInsensitive(root)
	if err != nil {
		t.Fatal(err)
	}
	ops, err := parsePatch("*** Begin Patch\n*** Add File: NewFile\n+one\n*** Add File: newfile\n+two\n*** End Patch")
	if err != nil {
		t.Fatal(err)
	}
	changes, _, err := planNativePatch(context.Background(), root, dir, ops)
	if fold {
		if err == nil {
			t.Fatal("case aliases passed preflight")
		}
		patch := "*** Begin Patch\n*** Update File: " + strings.ToUpper(dir) + "/update.txt\n-two\n+changed\n*** End Patch"
		if err := runPatch(context.Background(), dir, patch, io.Discard); err != nil {
			t.Fatal("valid absolute case spelling rejected", err)
		}
	} else {
		if err != nil {
			t.Fatal("distinct case paths rejected", err)
		}
		if err := applyNativePatch(context.Background(), root, changes); err != nil {
			t.Fatal(err)
		}
		for path, want := range map[string]string{"NewFile": "one\n", "newfile": "two\n"} {
			data, err := os.ReadFile(filepath.Join(dir, path))
			if err != nil || string(data) != want {
				t.Fatal(path, string(data), err)
			}
		}
	}
}

func TestPatchWritableChildDoesNotRequireWritableWorkingDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission proof requires an unprivileged user")
	}
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "writable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "writable", "file"), []byte("old\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(dir, 0755); err != nil {
			t.Error(err)
		}
	})
	err := runPatch(context.Background(), dir, "*** Begin Patch\n*** Update File: writable/file\n-old\n+new\n*** End Patch", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "writable", "file"))
	if err != nil || string(data) != "new\n" {
		t.Fatal(string(data), err)
	}
}
