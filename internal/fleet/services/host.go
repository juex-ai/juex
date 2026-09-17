package services

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"github.com/juex-ai/juex/internal/foundation/homestore"
	"github.com/juex-ai/juex/internal/foundation/processidentity"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

// Lease fences delayed children before they open business storage. App owns the
// lease for the entire service process, including recovery and graceful drain.
type Lease struct {
	home   string
	intent launchIntent
	lock   *homestore.Lock
}

func Acquire(home, id, instance string) (*Lease, error) {
	if err := serviceendpoint.ValidateID(id); err != nil {
		return nil, err
	}
	lock, err := serviceendpoint.StateLock(home, id)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Lease, error) { _ = lock.Close(); return nil, err }
	var intent launchIntent
	if err := serviceendpoint.ReadJSON(serviceendpoint.IntentPath(home, id), &intent); err != nil {
		return fail(err)
	}
	var desired desiredState
	if err := serviceendpoint.ReadJSON(desiredPath(home, id), &desired); err != nil {
		return fail(err)
	}
	if instance == "" || intent.Runtime.InstanceID != instance || intent.Runtime.ServiceID != id || desired.State != "running" {
		return fail(errors.New("service launch superseded or stopped"))
	}
	fleet, err := serviceendpoint.FleetID(home)
	if err != nil {
		return fail(err)
	}
	if intent.Runtime.FleetID != fleet {
		return fail(errors.New("service launch Fleet mismatch"))
	}
	return &Lease{home: home, intent: intent, lock: lock}, nil
}
func (l *Lease) Close() error                       { return l.lock.Close() }
func (l *Lease) Identity() serviceendpoint.Identity { return l.intent.Runtime.Identity }
func (l *Lease) Config() map[string]any             { return l.intent.Definition.Config }
func (l *Lease) StateDir() string {
	return serviceendpoint.StateDir(l.home, l.intent.Runtime.ServiceID)
}
func (l *Lease) Listen() (net.Listener, error) {
	record := l.intent.Runtime
	if record.Network == "unix" {
		if err := os.MkdirAll(filepath.Dir(record.Address), 0o700); err != nil {
			return nil, err
		}
		// Never unlink an existing configured socket: another Fleet may own it.
		// Default sockets are instance-specific; graceful net.Listener.Close
		// removes its own socket, while uncertain leftovers require inspection.
	}
	listener, err := net.Listen(record.Network, record.Address)
	if err == nil && record.Network == "unix" {
		err = os.Chmod(record.Address, 0o600)
		if err != nil {
			_ = listener.Close()
		}
	}
	return listener, err
}

// Ready writes an instance-specific candidate only after business recovery.
// Fleet probes this listener before atomically publishing public discovery.
func (l *Lease) Ready(listener net.Listener) (serviceendpoint.Record, error) {
	record := l.intent.Runtime
	record.Address = listener.Addr().String()
	record.PID = os.Getpid()
	fingerprint, err := processidentity.Fingerprint(record.PID)
	if err != nil {
		return record, fmt.Errorf("service process identity: %w", err)
	}
	record.ProcessIdentity = fingerprint
	err = serviceendpoint.WriteJSON(serviceendpoint.CandidatePath(l.home, record.ServiceID, record.InstanceID), record)
	return record, err
}
