//go:build darwin || linux

package legacy

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSourceGuardContendsWithAnotherProcess(t *testing.T) {
	if name := os.Getenv("JUEX_SOURCE_GUARD_TEST_LOCK"); name != "" {
		holdSourceLock(t, name)
		fmt.Println("locked")
		_, _ = os.Stdin.Read(make([]byte, 1))
		return
	}
	home, agents := sourceGuardFixture(t)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestSourceGuardContendsWithAnotherProcess$")
	cmd.Env = append(os.Environ(), "JUEX_SOURCE_GUARD_TEST_LOCK="+filepath.Join(home, "agents/def456/.module-resources.lock"))
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	if line, err := bufio.NewReader(output).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("lock helper: %q %v", line, err)
	}
	if guard, err := AcquireSourceGuard(home, agents); err == nil {
		_ = guard.Close()
		t.Fatal("live writer accepted")
	}
	if err := input.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	guard, err := AcquireSourceGuard(home, agents)
	if err != nil {
		t.Fatal(err)
	}
	_ = guard.Close()
}

func TestSourceGuardCaptureKeepsBytesAndRootIdentity(t *testing.T) {
	home, _ := legacyFleetFixture(t)
	for _, name := range []string{".", "agents/abc234", "services/memory"} {
		if err := os.Chmod(filepath.Join(home, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"fleet.lock", ".locks/fleet/supervisor-role.lock", ".locks/config-imports-cache.lock", ".locks/services/memory.lock", "services/memory/writer.lock", ".locks/fleet/abc234.lock", ".locks/agents/abc234.lock", ".locks/endpoints/abc234.lock", "agents/abc234/.module-resources.lock"} {
		p := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := fixtureHashes(t, home)
	g, err := AcquireSourceGuard(home, []string{"abc234"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = g.Close() }()
	fleet, err := g.Capture(home)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, fixtureHashes(t, home)) {
		t.Fatal("guard capture changed source")
	}
	// The borrowed-root reader must not follow a newly installed Home pathname.
	moved := home + "-held"
	if err := os.Rename(home, moved); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(moved) })
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "fleet.json"), []byte(`{"id":"replacement"}`), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readFleet(g.root, home, moved)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != fleet.ID || len(got.Agents) != len(fleet.Agents) {
		t.Fatal("borrowed source root followed replacement")
	}
	if _, err := g.Capture(moved); err == nil {
		t.Fatal("guard allowed changed public Home binding")
	}
}

func sourceGuardFixture(t *testing.T) (string, []string) {
	t.Helper()
	home := t.TempDir()
	if err := os.Chmod(home, 0700); err != nil {
		t.Fatal(err)
	}
	agents := []string{"abc234", "def456"}
	paths := []string{"fleet.lock", ".locks/fleet/supervisor-role.lock", ".locks/config-imports-cache.lock", ".locks/services/memory.lock", "services/memory/writer.lock"}
	for _, id := range agents {
		paths = append(paths, ".locks/fleet/"+id+".lock", ".locks/agents/"+id+".lock", ".locks/endpoints/"+id+".lock", "agents/"+id+"/.module-resources.lock")
	}
	for _, name := range paths {
		path := filepath.Join(home, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return home, agents
}

func holdSourceLock(t *testing.T, name string) *os.File {
	t.Helper()
	f, err := os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestSourceGuardFencesEveryWriterAndReleases(t *testing.T) {
	home, agents := sourceGuardFixture(t)
	g, err := AcquireSourceGuard(home, agents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Close() })
	if err := g.Check(); err != nil {
		t.Fatal(err)
	}
	// Independent descriptors must not acquire any of the concrete legacy locks.
	err = filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			t.Errorf("unfenced legacy writer: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if err := g.Check(); err == nil {
		t.Fatal("closed guard accepted")
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireSourceGuard(home, agents)
	if err != nil {
		t.Fatal(err)
	}
	_ = second.Close()
}

func TestSourceGuardFailureDoesNotCreateOrRetainLocks(t *testing.T) {
	for _, variant := range []string{"busy", "missing", "symlink", "fifo", "directory", "writable", "extra-agent", "missing-agent", "duplicate-agent", "invalid-agent"} {
		t.Run(variant, func(t *testing.T) {
			home, agents := sourceGuardFixture(t)
			baseline, err := AcquireSourceGuard(home, agents)
			if err != nil {
				t.Fatal("invalid baseline", err)
			}
			if err := baseline.Close(); err != nil {
				t.Fatal(err)
			}
			name := filepath.Join(home, "agents/def456/.module-resources.lock")
			switch variant {
			case "busy":
				holdSourceLock(t, name)
			case "missing":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
			case "symlink", "fifo", "directory":
				if err := os.Remove(name); err != nil {
					t.Fatal(err)
				}
				var err error
				switch variant {
				case "symlink":
					err = os.Symlink(filepath.Join(home, "fleet.lock"), name)
				case "fifo":
					err = unix.Mkfifo(name, 0600)
				case "directory":
					err = os.Mkdir(name, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "writable":
				if err := os.Chmod(name, 0666); err != nil {
					t.Fatal(err)
				}
			case "extra-agent":
				if err := os.Mkdir(filepath.Join(home, "agents/ghi567"), 0700); err != nil {
					t.Fatal(err)
				}
			case "missing-agent":
				agents = agents[:1]
			case "duplicate-agent":
				agents = append(agents, agents[0])
			case "invalid-agent":
				agents[1] = "../escape"
			}
			g, err := AcquireSourceGuard(home, agents)
			if err == nil {
				_ = g.Close()
				t.Fatal("invalid source accepted")
			}
			if g != nil {
				t.Fatal("partial guard returned")
			}
			holdSourceLock(t, filepath.Join(home, "fleet.lock"))
			if variant == "missing" {
				if _, err := os.Lstat(name); !os.IsNotExist(err) {
					t.Fatal("missing lock was created")
				}
			}
		})
	}
}

func TestSourceGuardDetectsPathAndRegistryChanges(t *testing.T) {
	for _, variant := range []string{"root", "directory", "directory-symlink", "lock", "lock-mode", "new-agent", "removed-agent"} {
		t.Run(variant, func(t *testing.T) {
			home, agents := sourceGuardFixture(t)
			g, err := AcquireSourceGuard(home, agents)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = g.Close() }()
			switch variant {
			case "root":
				if err := os.Rename(home, home+"-original"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.RemoveAll(home + "-original") })
				if err := os.Mkdir(home, 0700); err != nil {
					t.Fatal(err)
				}
			case "directory", "directory-symlink":
				path := filepath.Join(home, ".locks/endpoints")
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				if variant == "directory-symlink" {
					err = os.Symlink(path+"-original", path)
				} else {
					err = os.Mkdir(path, 0700)
				}
				if err != nil {
					t.Fatal(err)
				}
			case "lock":
				path := filepath.Join(home, "fleet.lock")
				if err := os.Rename(path, path+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "lock-mode":
				if err := os.Chmod(filepath.Join(home, "fleet.lock"), 0666); err != nil {
					t.Fatal(err)
				}
			case "new-agent":
				if err := os.Mkdir(filepath.Join(home, "agents/ghi567"), 0700); err != nil {
					t.Fatal(err)
				}
			case "removed-agent":
				if err := os.Rename(filepath.Join(home, "agents/abc234"), filepath.Join(home, "removed")); err != nil {
					t.Fatal(err)
				}
			}
			if err := g.Check(); err == nil {
				t.Fatal("changed source accepted")
			}
		})
	}
}
