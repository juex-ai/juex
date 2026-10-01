package management

import (
	"context"
	"fmt"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"time"
)

type PurgeRequest struct {
	ID      string `json:"id"`
	AgentID string `json:"agent_id"`
	Version int64  `json:"version"`
}

type PurgeJob struct {
	lifecycle.Target
	ActorID    string                       `json:"actor_id"`
	Version    int64                        `json:"version"`
	State      string                       `json:"state"`
	Step       int                          `json:"step"`
	Receipts   map[string]lifecycle.Receipt `json:"receipts"`
	Error      string                       `json:"error"`
	CreatedAt  time.Time                    `json:"created_at"`
	UpdatedAt  time.Time                    `json:"updated_at"`
	LeaseEpoch int64                        `json:"-"`
}

var purgeSteps = []struct{ Service, Phase string }{
	{"runtime", lifecycle.Fence}, {"memory", lifecycle.Fence}, {"calendar", lifecycle.Fence}, {"execution", lifecycle.Fence},
	{"execution", lifecycle.Erase}, {"memory", lifecycle.Erase}, {"calendar", lifecycle.Erase}, {"runtime", lifecycle.Erase},
}

type PurgeRepository interface {
	ClaimPurge(context.Context) (PurgeJob, bool, error)
	FinishPurgeStep(context.Context, PurgeJob, string, lifecycle.Receipt, string) error
}

type Purger struct {
	Repository PurgeRepository
	Services   map[string]lifecycle.Participant
}

// Reconcile advances one durable step. Lost replies repeat an idempotent step;
// no deletion phase starts until every service's local barrier is acknowledged.
func (p *Purger) Reconcile(ctx context.Context) (bool, error) {
	job, ok, err := p.Repository.ClaimPurge(ctx)
	if err != nil || !ok {
		return ok, err
	}
	if job.Step == len(purgeSteps) && job.State != "completed" {
		return true, p.Repository.FinishPurgeStep(ctx, job, "", lifecycle.Receipt{}, "")
	}
	service, phase := "execution", lifecycle.Erase
	if job.Step < len(purgeSteps) {
		service, phase = purgeSteps[job.Step].Service, purgeSteps[job.Step].Phase
	}
	participant := p.Services[service]
	var receipt lifecycle.Receipt
	if participant == nil {
		err = fmt.Errorf("cleanup service %s is unavailable", service)
	} else {
		call, cancel := context.WithTimeout(ctx, 25*time.Second)
		receipt, err = participant.Purge(call, lifecycle.Request{Target: job.Target, Phase: phase})
		cancel()
	}
	message := ""
	if err != nil {
		message = service + " cleanup unavailable; retrying"
	}
	if err == nil && !receipt.Fenced {
		message = service + " cleanup barrier unconfirmed; retrying"
	}
	if saveErr := p.Repository.FinishPurgeStep(ctx, job, service, receipt, message); saveErr != nil {
		return true, saveErr
	}
	return true, err
}
