package observable

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type sourceStopReason string

const (
	sourceStopUser          sourceStopReason = "user"
	sourceStopDelete        sourceStopReason = "delete"
	sourceStopShutdown      sourceStopReason = "shutdown"
	sourceStopFailedStartup sourceStopReason = "failed_startup"
)

func validSourceStopReason(reason sourceStopReason) bool {
	switch reason {
	case sourceStopUser, sourceStopDelete, sourceStopShutdown, sourceStopFailedStartup:
		return true
	default:
		return false
	}
}

type sourceStopResult struct {
	Quiesced bool
}

type terminalOutcome struct {
	State    string
	ExitCode *int
	Err      error
}

type sourceRuntime interface {
	start(context.Context, *observableRun) error
	stop(context.Context, *observableRun, sourceStopReason) (sourceStopResult, error)
	deleteState(context.Context, string) error
	statusSnapshot(ObservableStatus) ObservableStatus
}

type sourceKernel interface {
	activateRun(*observableRun, ObservableStatus) error
	finishRun(*observableRun, terminalOutcome) (bool, error)
	recordObservation(ObservationRecord) (ObservationRecord, bool, error)
	recordedObservations(string, string, int) ([]ObservationRecord, error)
	submitDelivery(context.Context, ObservationRecord) bool
	now() time.Time
	isClosed() bool
}

type scheduleStateStore interface {
	ScheduleState(string) (ScheduleStateRecord, bool, error)
	RecordScheduleState(ScheduleStateRecord) error
	ClearScheduleState(string) error
	DropRecordedScheduleObservations(string, string) error
}

type sourceDependencies struct {
	opts  ManagerOptions
	store *Store
}

type sourceRuntimeFactory func(Spec, sourceKernel, sourceDependencies) (sourceRuntime, error)

func newSourceRuntime(spec Spec, kernel sourceKernel, deps sourceDependencies) (sourceRuntime, error) {
	switch spec.SourceType() {
	case SourceTypeCommand:
		command, ok := spec.commandRuntime()
		if !ok {
			return nil, fmt.Errorf("observable %q has no command configuration", spec.ID)
		}
		return &commandSourceRuntime{spec: command, kernel: kernel, opts: deps.opts, store: deps.store}, nil
	case SourceTypeSchedule:
		schedule, ok := spec.scheduleRuntime()
		if !ok {
			return nil, fmt.Errorf("observable %q has no schedule configuration", spec.ID)
		}
		return &scheduleSourceRuntime{spec: schedule, kernel: kernel, store: deps.store}, nil
	default:
		return nil, fmt.Errorf("observable %q has unsupported source type %q", spec.ID, spec.SourceType())
	}
}

func statusFromSpec(spec Spec, state string) ObservableStatus {
	status := baseStatusFromSpec(spec, state)
	if commandSpec, ok := spec.commandRuntime(); ok {
		status.Command = commandSpec.Command
		status.Args = append([]string(nil), commandSpec.Args...)
		status.Streams = append([]string(nil), commandSpec.Streams...)
		status.Batch = commandSpec.Batch
	}
	if scheduleSpec, ok := spec.scheduleRuntime(); ok {
		status.Schedule = &ScheduleStatus{
			Summary:     scheduleSummary(scheduleSpec),
			Timezone:    scheduleSpec.Timezone,
			CatchUpMode: scheduleSpec.CatchUp.Mode,
		}
	}
	return status
}

func baseStatusFromSpec(spec Spec, state string) ObservableStatus {
	return ObservableStatus{
		ID:         spec.ID,
		Name:       spec.Name,
		SourceType: spec.SourceType(),
		State:      state,
	}
}

type terminalClaim struct {
	resolved chan struct{}
	once     sync.Once
}

func newTerminalClaim() *terminalClaim {
	return &terminalClaim{resolved: make(chan struct{})}
}

func (c *terminalClaim) resolve() {
	if c == nil {
		return
	}
	c.once.Do(func() { close(c.resolved) })
}

func (r *observableRun) closeQuiesced() {
	if r == nil || r.quiesced == nil {
		return
	}
	r.quiescedOnce.Do(func() { close(r.quiesced) })
}

func waitRunQuiesced(ctx context.Context, run *observableRun) error {
	if run == nil || run.quiesced == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-run.quiesced:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
