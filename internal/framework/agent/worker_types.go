package agent

import (
	"errors"

	"github.com/juex-ai/juex/internal/foundation/errorclass"
	"github.com/juex-ai/juex/internal/framework/thread"
)

var (
	ErrWorkerThreadNotActive     = errors.New("worker thread is not active")
	ErrWorkerThreadManagerClosed = errors.New("worker thread manager is closed")
	ErrWorkerThreadStopped       = errorclass.WithKind(errorclass.KindTerminated, errors.New("worker thread stopped"))
)

type WorkerThreadState string

const (
	WorkerThreadStateRunning  WorkerThreadState = "running"
	WorkerThreadStateIdle     WorkerThreadState = "idle"
	WorkerThreadStateFailed   WorkerThreadState = "failed"
	WorkerThreadStateStopping WorkerThreadState = "stopping"
)

type WorkerThreadStatus struct {
	ThreadID          string            `json:"thread_id"`
	Alias             string            `json:"alias,omitempty"`
	State             WorkerThreadState `json:"state"`
	Model             string            `json:"model,omitempty"`
	Subscribed        bool              `json:"subscribed"`
	PendingCount      int               `json:"pending_count"`
	LastTurnID        string            `json:"last_turn_id,omitempty"`
	LastResult        string            `json:"last_result,omitempty"`
	LastError         string            `json:"last_error,omitempty"`
	NotificationError string            `json:"notification_error,omitempty"`
	CreatedAt         thread.Timestamp  `json:"created_at"`
	UpdatedAt         thread.Timestamp  `json:"updated_at"`
}
