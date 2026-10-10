package execution

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// ManagedResource is Execution-owned infrastructure. Its credential is derived
// from a separate Execution key and is never returned through a resource API.
type ManagedResource struct {
	EnvironmentID, AgentID, TenantID, UserID, CredentialHash string
	Backend, OS, WorkingDirectory, HomeDirectory             string
	Slot                                                     uint16
	Memory, NanoCPUs                                         int64
	Running, Busy, Online                                    bool
	LastActivity                                             time.Time
	StorageIdentity                                          string
	ProjectID                                                uint32
	WorkspaceBytes, WorkspaceInodes                          int64
	Provisioned                                              bool
	Purging, PurgeData                                       bool
	Unconfirmed                                              bool
}

type ManagedResult struct {
	Running     bool
	Error       string
	Provisioned bool
	Purged      bool
}

type ManagedRepository interface {
	EnsureManaged(context.Context, Scope, ManagedResource) (ManagedResource, error)
	ManagedIDs(context.Context) ([]string, error)
	// LockManaged serializes lifecycle effects against dispatch. Admission and
	// device receipts remain available while work waits for the next wake.
	// A crash can repeat Ensure/Stop, never an operation's side effects.
	LockManaged(context.Context, string, func(ManagedResource) (ManagedResult, error)) error
}

type ManagedBackend interface {
	Resource(string) (ManagedResource, error)
	Ensure(context.Context, ManagedResource, string) error
	Stop(context.Context, ManagedResource) error
	// Purge stops the owned resource before removing data and tolerates a retry
	// after removal succeeded but the lifecycle transaction did not commit.
	Purge(context.Context, ManagedResource) error
}

type ManagedManager struct {
	Store     ManagedRepository
	Backend   ManagedBackend
	Authority Authority
	Key       []byte
	Idle      time.Duration
}

func (h *ManagedManager) credential(id, backend string) string {
	mac := hmac.New(sha256.New, h.Key)
	namespace := "juex.host.enrollment.v1\x00"
	if backend == "gvisor" {
		// Existing container journals remain bound to their original credential.
		namespace = "juex.hosted.enrollment.v1\x00"
	}
	_, _ = mac.Write([]byte(namespace + id))
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *ManagedManager) Ensure(ctx context.Context, scope Scope) error {
	_, err := h.ensureResource(ctx, scope)
	return err
}

func (h *ManagedManager) ensureResource(ctx context.Context, scope Scope) (ManagedResource, error) {
	if len(h.Key) != 32 || h.Backend == nil || h.Store == nil || !scope.CanExecute {
		return ManagedResource{}, execprotocol.ErrUnavailable
	}
	id := uuid.NewString()
	candidate, err := h.Backend.Resource(id)
	if err != nil {
		return ManagedResource{}, err
	}
	if err := candidate.validate(); err != nil || candidate.EnvironmentID != id {
		return ManagedResource{}, execprotocol.ErrInvalid
	}
	candidate.AgentID, candidate.TenantID, candidate.UserID = scope.AgentID, scope.TenantID, scope.UserID
	candidate.CredentialHash = Digest(h.credential(id, candidate.Backend))
	resource, err := h.Store.EnsureManaged(ctx, scope, candidate)
	if err != nil {
		return ManagedResource{}, err
	}
	return resource, h.matches(resource)
}

func (r ManagedResource) validate() error {
	if id, err := uuid.Parse(r.EnvironmentID); err != nil || id == uuid.Nil || id.String() != r.EnvironmentID {
		return execprotocol.ErrInvalid
	}
	if !filepath.IsAbs(r.WorkingDirectory) || !filepath.IsAbs(r.HomeDirectory) {
		return execprotocol.ErrInvalid
	}
	switch r.Backend {
	case "host":
		if r.OS != "darwin" && r.OS != "linux" || r.StorageIdentity != "" || r.ProjectID != 0 || r.Memory != 0 || r.NanoCPUs != 0 || r.WorkspaceBytes != 0 || r.WorkspaceInodes != 0 {
			return execprotocol.ErrInvalid
		}
	case "gvisor":
		if r.OS != "linux" || r.Memory < 128<<20 || r.NanoCPUs < 100000000 || r.WorkspaceBytes < 16<<20 || r.WorkspaceInodes < 64 {
			return execprotocol.ErrInvalid
		}
		if id, err := uuid.Parse(r.StorageIdentity); err != nil || id == uuid.Nil {
			return execprotocol.ErrInvalid
		}
	default:
		return execprotocol.ErrInvalid
	}
	return nil
}

func (h *ManagedManager) matches(resource ManagedResource) error {
	expected, err := h.Backend.Resource(resource.EnvironmentID)
	if err != nil {
		return err
	}
	if resource.Backend != expected.Backend || resource.OS != expected.OS || resource.WorkingDirectory != expected.WorkingDirectory || resource.HomeDirectory != expected.HomeDirectory || resource.StorageIdentity != expected.StorageIdentity {
		return errors.New("managed environment configuration does not match persisted ownership")
	}
	if len(h.Key) != 32 || resource.CredentialHash != Digest(h.credential(resource.EnvironmentID, resource.Backend)) {
		return errors.New("managed enrollment key does not match persisted environment")
	}
	return nil
}

func (h *ManagedManager) Reconcile(ctx context.Context) error {
	ids, err := h.Store.ManagedIDs(ctx)
	if err != nil {
		return err
	}
	idle := h.Idle
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	var failures []error
	for _, id := range ids {
		err := h.Store.LockManaged(ctx, id, func(resource ManagedResource) (ManagedResult, error) {
			result := ManagedResult{Running: resource.Running, Provisioned: resource.Provisioned}
			if err := h.matches(resource); err != nil {
				return result, err
			}
			if resource.Purging {
				if resource.Backend == "host" && resource.Unconfirmed {
					// Keep the original journal connected until cancellation is
					// confirmed. A native stop cannot prove external processes died.
					if !resource.Online {
						if err := h.Backend.Ensure(ctx, resource, h.credential(resource.EnvironmentID, resource.Backend)); err != nil {
							return result, err
						}
						result.Provisioned = true
					}
					result.Running = true
					return result, nil
				}
				if resource.PurgeData {
					// Purge owns stopping and removal, including retries after files
					// were removed but the database transaction did not commit.
					if err := h.Backend.Purge(ctx, resource); err != nil {
						return result, err
					}
					result.Purged = true
				} else if err := h.Backend.Stop(ctx, resource); err != nil {
					return result, err
				}
				result.Running = false
				return result, nil
			}
			scope, err := h.Authority.Agent(ctx, resource.UserID, resource.TenantID, resource.AgentID, true)
			if err != nil && !errors.Is(err, execprotocol.ErrDenied) {
				return result, err
			}
			active := err == nil && scope.CanExecute
			if resource.Busy {
				// Even a suspended Agent may need its existing journal to drain
				// cancellations/results. The broker independently rejects new work.
				if resource.Online {
					result.Running = true
					return result, nil
				}
				credential := h.credential(resource.EnvironmentID, resource.Backend)
				if len(h.Key) != 32 || Digest(credential) != resource.CredentialHash {
					return result, errors.New("managed enrollment key mismatch")
				}
				if err := h.Backend.Ensure(ctx, resource, credential); err != nil {
					result.Error = "managed environment could not start; check Execution service logs"
					failures = append(failures, err)
					return result, nil
				}
				result.Running = true
				result.Provisioned = true
			} else if resource.Running && (!active || time.Since(resource.LastActivity) >= idle) {
				if err := h.Backend.Stop(ctx, resource); err != nil {
					return result, err
				}
				result.Running = false
			}
			return result, nil
		})
		if err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
