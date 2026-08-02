package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/extensions"
)

func TestExtensionRuntimeContextUsesAgentOwnedDataDirectory(t *testing.T) {
	home := t.TempDir()
	firstAddress, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	secondAddress, err := agentstate.NewAgentAddress(home, "ijklmn")
	if err != nil {
		t.Fatal(err)
	}
	extension := extensions.Extension{
		Name:   "demo",
		Dir:    filepath.Join(t.TempDir(), "installed demo"),
		Source: extensions.Source("demo"),
	}

	first := newExtensionRuntimeContext(firstAddress, extension)
	second := newExtensionRuntimeContext(secondAddress, extension)
	if got, want := first.DataDir, filepath.Join(firstAddress.StateDir(), "extensions", "demo"); got != want {
		t.Fatalf("first data dir = %q, want %q", got, want)
	}
	if got, want := second.DataDir, filepath.Join(secondAddress.StateDir(), "extensions", "demo"); got != want {
		t.Fatalf("second data dir = %q, want %q", got, want)
	}
	if first.DataDir == second.DataDir {
		t.Fatalf("agent data directories are not isolated: %q", first.DataDir)
	}
	otherExtension := newExtensionRuntimeContext(firstAddress, extensions.Extension{Name: "other"})
	if otherExtension.DataDir == first.DataDir {
		t.Fatalf("extension data directories are not isolated: %q", first.DataDir)
	}
	movedWorkspace := newExtensionRuntimeContext(firstAddress, extensions.Extension{
		Name:   "demo",
		Dir:    filepath.Join(t.TempDir(), "moved workspace", "demo"),
		Source: extensions.Source("demo"),
	})
	if movedWorkspace.DataDir != first.DataDir {
		t.Fatalf("workspace move changed Agent-owned data dir: %q != %q", movedWorkspace.DataDir, first.DataDir)
	}
	if first.ExtensionDir != extension.Dir || first.Source != extension.Source || first.ExtensionName != extension.Name {
		t.Fatalf("runtime metadata = %+v", first)
	}
	if got := first.AdditionalWritableRoots(); len(got) != 1 || got[0] != first.DataDir {
		t.Fatalf("additional writable roots = %#v", got)
	}

	stateFree := newExtensionRuntimeContext(agentstate.AgentAddress{}, extension)
	if stateFree.DataDir != "" || len(stateFree.AdditionalWritableRoots()) != 0 {
		t.Fatalf("state-free runtime context = %+v", stateFree)
	}
}

func TestExtensionRuntimeContextPrepareDataDirIsPrivateAndPersistent(t *testing.T) {
	home := t.TempDir()
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(address.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	context := newExtensionRuntimeContext(address, extensions.Extension{
		Name:   "demo",
		Dir:    filepath.Join(t.TempDir(), "demo"),
		Source: extensions.Source("demo"),
	})

	if err := context.PrepareDataDir(); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(context.DataDir, "marker")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(context.DataDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := context.PrepareDataDir(); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "keep" {
		t.Fatalf("persistent marker = %q, %v", got, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(context.DataDir)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o700 {
			t.Fatalf("data dir mode = %o, want 700", got)
		}
	}
}

func TestExtensionRuntimeContextPrepareDataDirIsConcurrent(t *testing.T) {
	home := t.TempDir()
	address, err := agentstate.NewAgentAddress(home, "abcdef")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(address.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	context := newExtensionRuntimeContext(address, extensions.Extension{Name: "demo"})

	const workers = 64
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- context.PrepareDataDir()
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent PrepareDataDir() error = %v", err)
		}
	}
	if info, err := os.Stat(context.DataDir); err != nil || !info.IsDir() {
		t.Fatalf("concurrent data dir info = %+v, %v", info, err)
	}
}

func TestExtensionRuntimeContextRejectsSymlinkEscape(t *testing.T) {
	for _, target := range []string{"extensions-root", "extension-dir"} {
		t.Run(target, func(t *testing.T) {
			home := t.TempDir()
			address, err := agentstate.NewAgentAddress(home, "abcdef")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(address.StateDir(), 0o700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			extensionsRoot := filepath.Join(address.StateDir(), "extensions")
			switch target {
			case "extensions-root":
				if err := os.Symlink(outside, extensionsRoot); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "extension-dir":
				if err := os.Mkdir(extensionsRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, filepath.Join(extensionsRoot, "demo")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}

			context := newExtensionRuntimeContext(address, extensions.Extension{Name: "demo"})
			err = context.PrepareDataDir()
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("PrepareDataDir() error = %v, want symlink rejection", err)
			}
		})
	}
}
