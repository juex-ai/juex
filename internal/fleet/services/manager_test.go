package services

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/homestore"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

func TestStoppedDesiredStateSurvivesManagerAndFencesDelayedChild(t *testing.T) {
	home := t.TempDir()
	def := Definition{Mode: Managed, Enabled: true, Command: []string{"missing-test-command"}}
	manager, err := New(Options{Home: home, Definitions: map[string]Definition{"memory": def}, StartTimeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Stop(context.Background(), "memory"); err != nil {
		t.Fatal(err)
	}
	another, err := New(Options{Home: home, Definitions: map[string]Definition{"memory": def}})
	if err != nil {
		t.Fatal(err)
	}
	statuses := another.Reconcile(context.Background())
	if len(statuses) != 1 || statuses[0].Phase != "stopped" || statuses[0].Desired != "stopped" {
		t.Fatalf("reconcile: %+v", statuses)
	}
	if _, err := Acquire(home, "memory", "old-instance"); err == nil {
		t.Fatal("delayed child started after stop")
	}
}

func TestOccupiedStateWithoutDiscoveryNeverLaunches(t *testing.T) {
	home := t.TempDir()
	manager, err := New(Options{Home: home, Definitions: map[string]Definition{"memory": {Mode: Managed, Enabled: true, Command: []string{"missing-test-command"}}}, StartTimeout: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := serviceendpoint.StateLock(home, "memory")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	_, err = manager.Start(context.Background(), "memory")
	if !errors.Is(err, homestore.ErrLockBusy) {
		t.Fatalf("uncertain writer: %v", err)
	}
	if _, err := os.Stat(serviceendpoint.IntentPath(home, "memory")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("intent overwritten: %v", err)
	}
}

func TestExternalLifecycleCannotStopRemoteProcess(t *testing.T) {
	manager, err := New(Options{Home: t.TempDir(), Definitions: map[string]Definition{"memory": {Mode: External, Enabled: true, Network: "tcp", Address: "127.0.0.1:9"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Stop(context.Background(), "memory"); err == nil {
		t.Fatal("external stop accepted")
	}
	if _, err := manager.Restart(context.Background(), "memory"); err == nil {
		t.Fatal("external restart accepted")
	}
}

func TestDelayedChildCannotAcquireSupersededLaunch(t *testing.T) {
	home := t.TempDir()
	manager, err := New(Options{Home: home, Definitions: map[string]Definition{"memory": {Mode: Managed, Enabled: true, Command: []string{"unused"}}}})
	if err != nil {
		t.Fatal(err)
	}
	old := manager.newIntent("memory", manager.definitions["memory"])
	replacement := manager.newIntent("memory", manager.definitions["memory"])
	if err := serviceendpoint.WriteJSON(desiredPath(home, "memory"), desiredState{State: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := serviceendpoint.WriteJSON(serviceendpoint.IntentPath(home, "memory"), replacement); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(home, "memory", old.Runtime.InstanceID); err == nil {
		t.Fatal("superseded child acquired storage")
	}
	lease, err := Acquire(home, "memory", replacement.Runtime.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	if _, err := Acquire(home, "memory", replacement.Runtime.InstanceID); !errors.Is(err, homestore.ErrLockBusy) {
		t.Fatalf("duplicate writer: %v", err)
	}
}

func TestRestartBudgetPersistsAcrossManagers(t *testing.T) {
	home := t.TempDir()
	options := Options{Home: home, Definitions: map[string]Definition{"memory": {Mode: Managed, Enabled: true, Command: []string{"missing-service-executable-for-test"}}}}
	for i := 0; i < 5; i++ {
		manager, err := New(options)
		if err != nil {
			t.Fatal(err)
		}
		manager.Reconcile(context.Background())
	}
	var desired desiredState
	if err := serviceendpoint.ReadJSON(desiredPath(home, "memory"), &desired); err != nil {
		t.Fatal(err)
	}
	if desired.Attempts != 3 {
		t.Fatalf("restart attempts: %+v", desired)
	}
}

func TestServiceListenerDoesNotReplaceAnotherFleetsSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix socket isolation")
	}
	home := t.TempDir()
	path := serviceendpoint.SocketPath(home, "fleet", "memory", "shared")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	lease := &Lease{intent: launchIntent{Runtime: serviceendpoint.Record{Network: "unix", Address: path}}}
	if other, err := lease.Listen(); err == nil {
		_ = other.Close()
		t.Fatal("another Fleet socket was replaced")
	}
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("original listener lost: %v", err)
	}
	_ = conn.Close()
}
