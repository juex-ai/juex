package serviceendpoint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFileResolverIsolatesFleetAndValidatesRecords(t *testing.T) {
	home := t.TempDir()
	id, err := FleetID(home)
	if err != nil {
		t.Fatal(err)
	}
	again, err := FleetID(home)
	if err != nil || id != again {
		t.Fatalf("identity changed: %q %q %v", id, again, err)
	}
	other, err := FleetID(t.TempDir())
	if err != nil || id == other {
		t.Fatalf("fleet isolation: %q %q %v", id, other, err)
	}
	resolver := FileResolver{Home: home, Fleet: id}
	if _, err := resolver.Resolve(context.Background(), "memory"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	record := Record{Identity: Identity{FleetID: id, ServiceID: "memory", InstanceID: "instance-1"}, Network: "tcp", Address: "127.0.0.1:5000"}
	if err := Publish(home, record); err != nil {
		t.Fatal(err)
	}
	got, err := resolver.Resolve(context.Background(), "memory")
	if err != nil || got.Identity != record.Identity {
		t.Fatalf("resolve: %+v %v", got, err)
	}
	record.FleetID = other
	if err := Publish(home, record); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), "memory"); err == nil {
		t.Fatal("cross-fleet record accepted")
	}
	if _, err := resolver.Resolve(context.Background(), "../escape"); err == nil {
		t.Fatal("invalid service accepted")
	}
}

func TestShortSocketPathsRemainFleetScoped(t *testing.T) {
	a := SocketPath(filepath.Join(t.TempDir(), string(make([]byte, 120))), "fleet-a", "memory", "instance")
	b := SocketPath("another-home", "fleet-b", "memory", "instance")
	if len(a) > 100 || a == b {
		t.Fatalf("socket paths: %q %q", a, b)
	}
}
