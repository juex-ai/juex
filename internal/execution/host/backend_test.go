package host

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/execution"
	"github.com/juex-ai/juex/internal/execution/hostservice"
)

type serviceProbe struct {
	running bool
	id      string
	starts  int
}

func (p *serviceProbe) Start(context.Context) (hostservice.Status, error) {
	p.running = true
	p.starts++
	return p.Status()
}
func (p *serviceProbe) Stop(context.Context) error   { p.running = false; return nil }
func (p *serviceProbe) Remove(context.Context) error { p.running = false; return nil }
func (p *serviceProbe) Status() (hostservice.Status, error) {
	return hostservice.Status{Running: p.running, EnvironmentID: p.id}, nil
}

func hostFixture(t *testing.T) (*Backend, execution.ManagedResource, *serviceProbe) {
	t.Helper()
	c := Config{Root: t.TempDir(), ControlRoot: t.TempDir(), Identity: uuid.NewString(), Executable: os.Args[0], Server: "http://127.0.0.1:8683", InsecureHTTP: true}
	c.Root, _ = filepath.EvalSymlinks(c.Root)
	c.ControlRoot, _ = filepath.EvalSymlinks(c.ControlRoot)
	for _, path := range []string{c.Root, c.ControlRoot} {
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	b, err := New(c)
	if err != nil {
		t.Fatal(err)
	}
	r, err := b.Resource(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	r.AgentID, r.TenantID, r.UserID = uuid.NewString(), uuid.NewString(), uuid.NewString()
	p := &serviceProbe{id: r.EnvironmentID}
	b.service = func(string) (localService, error) { return p, nil }
	return b, r, p
}

func TestHostProvisioningRetainsOwnedDirectoriesAndEnrollment(t *testing.T) {
	b, r, p := hostFixture(t)
	ctx := context.Background()
	if err := b.Ensure(ctx, r, "private credential"); err != nil {
		t.Fatal(err)
	}
	if p.starts != 1 {
		t.Fatal(p.starts)
	}
	proof := filepath.Join(r.HomeDirectory, "retained")
	if err := os.WriteFile(proof, []byte("user state"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.Ensure(ctx, r, "private credential"); err != nil || p.starts != 1 {
		t.Fatal("running executor restarted", err, p.starts)
	}
	if err := b.Ensure(ctx, r, "changed credential"); err == nil {
		t.Fatal("replaced enrollment")
	}
	if err := b.Stop(ctx, r); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(proof); err != nil || string(data) != "user state" {
		t.Fatal(string(data), err)
	}
	if err := b.Ensure(ctx, r, "private credential"); err != nil || p.starts != 2 {
		t.Fatal(err, p.starts)
	}
}

func TestOfflineHostOpenNeverInitializesMissingOwnership(t *testing.T) {
	b, _, _ := hostFixture(t)
	if _, err := OpenExisting(b.config); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(b.config.ControlRoot, "owner.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenExisting(b.config); err == nil {
		t.Fatal("adopted an unowned directory during offline maintenance")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("offline open recreated ownership: %v", err)
	}
}

func TestHostPurgeRejectsSymlinkAndUnownedDirectory(t *testing.T) {
	b, r, _ := hostFixture(t)
	ctx := context.Background()
	if err := b.Ensure(ctx, r, "private credential"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(b.config.Root, r.EnvironmentID)
	if err := os.Rename(data, data+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, data); err != nil {
		t.Fatal(err)
	}
	if err := b.Purge(ctx, r); err == nil {
		t.Fatal("followed user symlink")
	}
	if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(data); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(data+"-original", data); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(data, "owner.json")
	if err := os.WriteFile(marker, []byte(`{"environment_id":"other"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.Purge(ctx, r); err == nil {
		t.Fatal("deleted unowned directory")
	}
}

func TestHostPurgeKeepsMissingOwnershipAndUnknownJournal(t *testing.T) {
	for _, mode := range []string{"unknown", "missing-owner", "confirmed"} {
		t.Run(mode, func(t *testing.T) {
			b, r, _ := hostFixture(t)
			ctx := context.Background()
			if err := b.Ensure(ctx, r, "private credential"); err != nil {
				t.Fatal(err)
			}
			data := filepath.Join(b.config.Root, r.EnvironmentID)
			if mode == "unknown" {
				r.Unconfirmed = true
			}
			if mode == "missing-owner" {
				if err := os.Remove(filepath.Join(data, "owner.json")); err != nil {
					t.Fatal(err)
				}
			}
			err := b.Purge(ctx, r)
			if mode != "confirmed" {
				if err == nil {
					t.Fatal("uncertain ownership or outcome was erased")
				}
				if _, err := os.Stat(data); err != nil {
					t.Fatal("data was removed", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := b.Purge(ctx, r); err != nil {
				t.Fatal("cleanup could not resume after deletion", err)
			}
			if _, err := os.Stat(data); !os.IsNotExist(err) {
				t.Fatal("owned data retained", err)
			}
		})
	}
}
