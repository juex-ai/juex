package execution

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

// HostedResource is Execution-owned infrastructure. Its credential is derived
// from a separate Execution key and is never returned through a resource API.
type HostedResource struct {
	EnvironmentID, AgentID, TenantID, UserID, CredentialHash string
	Slot                                                     uint16
	Memory, NanoCPUs                                         int64
	Running, Busy, Online                                    bool
	LastActivity                                             time.Time
}

type HostedResult struct {
	Running bool
	Error   string
}

type HostedRepository interface {
	EnsureHosted(context.Context, Scope, HostedResource) (HostedResource, error)
	HostedIDs(context.Context) ([]string, error)
	// LockHosted serializes lifecycle effects against admission and device
	// writes. A crash can repeat Ensure/Stop, never an operation's side effects.
	LockHosted(context.Context, string, func(HostedResource) (HostedResult, error)) error
}

type HostedBackend interface {
	Ensure(context.Context, HostedResource, string) error
	Stop(context.Context, HostedResource) error
}

type HostedManager struct {
	Store     HostedRepository
	Backend   HostedBackend
	Authority Authority
	Key       []byte
	Idle      time.Duration
	Memory    int64
	NanoCPUs  int64
}

func (h *HostedManager) credential(id string) string {
	mac := hmac.New(sha256.New, h.Key)
	_, _ = mac.Write([]byte("juex.hosted.enrollment.v1\x00" + id))
	return hex.EncodeToString(mac.Sum(nil))
}

func (h *HostedManager) Ensure(ctx context.Context, scope Scope) error {
	if len(h.Key) != 32 || h.Backend == nil || h.Store == nil || !scope.CanExecute {
		return execprotocol.ErrUnavailable
	}
	id := uuid.NewString()
	memory, cpu := h.Memory, h.NanoCPUs
	if memory == 0 {
		memory = 768 << 20
	}
	if cpu == 0 {
		cpu = 1000000000
	}
	resource, err := h.Store.EnsureHosted(ctx, scope, HostedResource{EnvironmentID: id, AgentID: scope.AgentID, TenantID: scope.TenantID, UserID: scope.UserID, CredentialHash: Digest(h.credential(id)), Memory: memory, NanoCPUs: cpu})
	if err == nil && resource.CredentialHash != Digest(h.credential(resource.EnvironmentID)) {
		return errors.New("hosted enrollment key does not match persisted environment")
	}
	return err
}

func (h *HostedManager) Reconcile(ctx context.Context) error {
	ids, err := h.Store.HostedIDs(ctx)
	if err != nil {
		return err
	}
	idle := h.Idle
	if idle <= 0 {
		idle = 5 * time.Minute
	}
	var failures []error
	for _, id := range ids {
		err := h.Store.LockHosted(ctx, id, func(resource HostedResource) (HostedResult, error) {
			result := HostedResult{Running: resource.Running}
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
				credential := h.credential(resource.EnvironmentID)
				if len(h.Key) != 32 || Digest(credential) != resource.CredentialHash {
					return result, errors.New("hosted enrollment key mismatch")
				}
				if err := h.Backend.Ensure(ctx, resource, credential); err != nil {
					result.Error = "hosted environment could not start; check Execution service logs"
					failures = append(failures, err)
					return result, nil
				}
				result.Running = true
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
