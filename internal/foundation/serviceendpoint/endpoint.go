// Package serviceendpoint provides Fleet-scoped discovery and verified service control.
package serviceendpoint

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/homestore"
)

type Identity struct {
	FleetID    string `json:"fleet_id"`
	ServiceID  string `json:"service_id"`
	InstanceID string `json:"instance_id"`
}

type Record struct {
	Identity
	Network         string    `json:"network"`
	Address         string    `json:"address"`
	PID             int       `json:"pid,omitempty"`
	ProcessIdentity string    `json:"process_identity,omitempty"`
	StartedAt       time.Time `json:"started_at"`
}

type Resolver interface {
	Resolve(context.Context, string) (Record, error)
}

type FileResolver struct{ Home, Fleet string }

func (r FileResolver) Resolve(ctx context.Context, service string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	if err := ValidateID(service); err != nil {
		return Record{}, err
	}
	var record Record
	if err := ReadJSON(RuntimePath(r.Home, service), &record); err != nil {
		return record, err
	}
	if record.FleetID != r.Fleet || record.ServiceID != service || record.InstanceID == "" {
		return Record{}, errors.New("service endpoint identity mismatch")
	}
	return record, record.Validate()
}

var idPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func ValidateID(id string) error {
	if !idPattern.MatchString(id) {
		return fmt.Errorf("invalid service identity %q", id)
	}
	return nil
}
func (r Record) Validate() error {
	if r.FleetID == "" || r.InstanceID == "" {
		return errors.New("service endpoint requires fleet and instance identities")
	}
	if err := ValidateID(r.ServiceID); err != nil {
		return err
	}
	return ValidateAddress(r.Network, r.Address)
}
func ValidateAddress(network, address string) error {
	switch network {
	case "unix":
		if !filepath.IsAbs(address) || strings.ContainsRune(address, 0) || len(address) > 100 {
			return fmt.Errorf("invalid Unix service socket %q", address)
		}
	case "tcp":
		if _, _, err := net.SplitHostPort(address); err != nil {
			return fmt.Errorf("invalid TCP service address: %w", err)
		}
	default:
		return fmt.Errorf("unsupported service network %q", network)
	}
	return nil
}
func NewID() string { return hex.EncodeToString(randomBytes()) }
func randomBytes() []byte {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func FleetID(home string) (string, error) {
	lock, err := homestore.AcquireLock(filepath.Join(home, ".locks", "fleet-identity.lock"), homestore.LockWait)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Close() }()
	path := filepath.Join(home, "fleet.json")
	var identity struct {
		ID string `json:"id"`
	}
	if err := ReadJSON(path, &identity); err == nil {
		if identity.ID == "" {
			return "", errors.New("empty Fleet identity")
		}
		return identity.ID, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	identity.ID = NewID()
	return identity.ID, WriteJSON(path, identity)
}
func RuntimePath(home, service string) string {
	return filepath.Join(home, "run", "services", service+".json")
}
func StateDir(home, service string) string { return filepath.Join(home, "services", service) }
func IntentPath(home, service string) string {
	return filepath.Join(StateDir(home, service), "launch.json")
}
func CandidatePath(home, service, instance string) string {
	return filepath.Join(StateDir(home, service), "ready-"+instance+".json")
}
func StateLock(home, service string) (*homestore.Lock, error) {
	return homestore.AcquireLock(filepath.Join(StateDir(home, service), "writer.lock"), homestore.LockTry)
}
func LifecycleLock(home, service string) (*homestore.Lock, error) {
	return homestore.AcquireLock(filepath.Join(home, ".locks", "services", service+".lock"), homestore.LockTry)
}
func Publish(home string, record Record) error {
	if err := record.Validate(); err != nil {
		return err
	}
	return WriteJSON(RuntimePath(home, record.ServiceID), record)
}

// Remove only removes the inspected instance. Lifecycle writers serialize this
// check and unlink with their service lock.
func Remove(home string, expected Record) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	path := RuntimePath(home, expected.ServiceID)
	var current Record
	if err := ReadJSON(path, &current); err != nil {
		return err
	}
	if current.Identity != expected.Identity {
		return errors.New("service instance changed before discovery cleanup")
	}
	return os.Remove(path)
}
func ReadJSON(path string, value any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, value)
}
func WriteJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return homestore.WriteFileAtomic(path, append(data, '\n'), 0o600, 0o700)
}
func SocketPath(home, fleet, service, instance string) string {
	path := filepath.Join(home, "run", "sockets", service+"-"+instance+".sock")
	if len(path) <= 100 {
		return path
	}
	hash := sha256.Sum256([]byte(fleet + ":" + home + ":" + service + ":" + instance))
	return filepath.Join(os.TempDir(), "juex-sockets", hex.EncodeToString(hash[:16])+".sock")
}
