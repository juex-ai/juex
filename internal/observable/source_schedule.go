package observable

import (
	"context"
	"fmt"
	"time"

	"github.com/juex-ai/juex/internal/eventmedia"
)

const scheduleRecoveryLimit = 100

type scheduleSourceRuntime struct {
	spec   scheduleRuntimeSpec
	kernel sourceKernel
	store  scheduleStateStore
}

func (s *scheduleSourceRuntime) start(callCtx context.Context, run *observableRun) error {
	if err := s.evaluateStartup(callCtx, run); err != nil {
		run.closeQuiesced()
		_, _ = s.stop(context.Background(), run, sourceStopFailedStartup)
		_, _ = s.kernel.finishRun(run, terminalOutcome{State: RunStateErrored, Err: err})
		run.closeDone()
		return err
	}
	status := run.state
	status.State = RunStateRunning
	if err := s.kernel.activateRun(run, status); err != nil {
		run.closeQuiesced()
		_, _ = s.stop(context.Background(), run, sourceStopFailedStartup)
		_, _ = s.kernel.finishRun(run, terminalOutcome{State: RunStateErrored, Err: err})
		run.closeDone()
		return err
	}
	go s.loop(run)
	return nil
}

func (s *scheduleSourceRuntime) stop(ctx context.Context, run *observableRun, reason sourceStopReason) (sourceStopResult, error) {
	if run == nil {
		return sourceStopResult{Quiesced: true}, nil
	}
	if !validSourceStopReason(reason) {
		return sourceStopResult{}, fmt.Errorf("observable: unsupported stop reason %q", reason)
	}
	if run.cancel != nil {
		run.cancel()
	}
	if err := waitRunQuiesced(ctx, run); err != nil {
		return sourceStopResult{}, err
	}
	if reason == sourceStopUser {
		if err := s.recordPausedState(run.id, s.kernel.now()); err != nil {
			return sourceStopResult{Quiesced: true}, err
		}
	}
	return sourceStopResult{Quiesced: true}, nil
}

func (s *scheduleSourceRuntime) deleteState(_ context.Context, id string) error {
	if s.store == nil {
		return nil
	}
	if err := s.store.ClearScheduleState(id); err != nil {
		return err
	}
	return s.store.DropRecordedScheduleObservations(id, "observable deleted")
}

func (s *scheduleSourceRuntime) statusSnapshot(status ObservableStatus) ObservableStatus {
	schedule := &ScheduleStatus{
		Summary:     scheduleSummary(s.spec),
		Timezone:    s.spec.Timezone,
		CatchUpMode: s.spec.CatchUp.Mode,
	}
	if s.store != nil {
		if state, ok, err := s.store.ScheduleState(status.ID); err == nil && ok {
			if !state.LastEvaluatedAt.IsZero() {
				value := state.LastEvaluatedAt
				schedule.LastEvaluatedAt = &value
			}
			if !state.LastEmittedScheduledAt.IsZero() {
				value := state.LastEmittedScheduledAt
				schedule.LastEmittedScheduledAt = &value
			}
			if next, found, nextErr := nextScheduledOccurrence(s.spec, state, s.kernel.now()); nextErr == nil && found {
				value := next.ScheduledAt
				schedule.NextOccurrence = &value
			}
		} else if next, found, nextErr := nextScheduledOccurrence(s.spec, ScheduleStateRecord{}, s.kernel.now()); nextErr == nil && found {
			value := next.ScheduledAt
			schedule.NextOccurrence = &value
		}
	}
	status.Schedule = schedule
	return status
}

func (s *scheduleSourceRuntime) evaluateStartup(ctx context.Context, run *observableRun) error {
	if s.store == nil {
		return nil
	}
	now := s.kernel.now()
	state, ok, err := s.store.ScheduleState(run.id)
	if err != nil {
		return err
	}
	if ok && state.Paused {
		return s.recordState(run.id, now, state.LastEmittedScheduledAt)
	}
	if ok {
		if err := s.recoverRecorded(ctx, run); err != nil {
			return err
		}
	}
	if !ok || state.LastEvaluatedAt.IsZero() {
		return s.recordState(run.id, now, time.Time{})
	}
	latest, missed, err := latestMissedScheduledOccurrence(s.spec, state, now)
	if err != nil {
		return err
	}
	if missed && catchUpAllows(s.spec, latest, now) {
		_, emitted, emitErr := s.emitOccurrence(ctx, run, latest, now)
		if emitErr != nil {
			return emitErr
		}
		if emitted {
			return nil
		}
		return s.recordState(run.id, now, latest.ScheduledAt)
	}
	if !missed && shouldPreserveIntervalStartupBaseline(s.spec, state) {
		return s.recordState(run.id, state.LastEvaluatedAt, state.LastEmittedScheduledAt)
	}
	return s.recordState(run.id, now, state.LastEmittedScheduledAt)
}

func (s *scheduleSourceRuntime) recoverRecorded(ctx context.Context, run *observableRun) error {
	records, err := s.kernel.recordedObservations(run.id, scheduleSourceEventPrefix(run.id), scheduleRecoveryLimit)
	if err != nil {
		return err
	}
	deliverCtx := context.Background()
	if ctx != nil {
		deliverCtx = context.WithoutCancel(ctx)
	}
	for i := len(records) - 1; i >= 0; i-- {
		s.kernel.submitDelivery(deliverCtx, records[i])
	}
	return nil
}

func (s *scheduleSourceRuntime) loop(run *observableRun) {
	defer run.closeDone()
	var outcome terminalOutcome
	for {
		state, _, err := s.store.ScheduleState(run.id)
		if err != nil {
			outcome = terminalOutcome{State: RunStateErrored, Err: err}
			break
		}
		next, ok, err := nextScheduledOccurrence(s.spec, state, s.kernel.now())
		if err != nil {
			outcome = terminalOutcome{State: RunStateErrored, Err: err}
			break
		}
		if !ok {
			outcome = terminalOutcome{State: RunStateExited}
			break
		}
		delay := time.Until(next.ScheduledAt)
		if delay < 0 {
			delay = 0
		}
		timer := time.NewTimer(delay)
		select {
		case <-run.ctx.Done():
			stopScheduleTimer(timer)
			outcome = terminalOutcome{State: RunStateExited, Err: run.ctx.Err()}
			run.closeQuiesced()
			_, _ = s.kernel.finishRun(run, outcome)
			return
		case <-timer.C:
			if _, _, err := s.emitOccurrence(context.Background(), run, next, s.kernel.now()); err != nil {
				outcome = terminalOutcome{State: RunStateErrored, Err: err}
				run.closeQuiesced()
				_, _ = s.kernel.finishRun(run, outcome)
				return
			}
		}
	}
	run.closeQuiesced()
	_, _ = s.kernel.finishRun(run, outcome)
}

func (s *scheduleSourceRuntime) emitOccurrence(ctx context.Context, run *observableRun, occurrence ScheduledOccurrence, observedAt time.Time) (ObservationRecord, bool, error) {
	observedAt = normalizeNow(observedAt)
	record, created, err := s.kernel.recordObservation(ObservationRecord{
		ObservableID:  run.id,
		RunID:         run.runID,
		SourceEventID: occurrence.SourceEventID,
		Kind:          resolvedKind(s.spec.Observation.Kind),
		Severity:      resolvedSeverity(s.spec.Observation.Severity),
		WindowStart:   occurrence.ScheduledAt,
		WindowEnd:     observedAt,
		Content:       s.spec.Observation.Content,
		Attachments:   append([]eventmedia.AttachmentRef(nil), s.spec.Observation.Attachments...),
		State:         ObservationStateRecorded,
	})
	if err != nil {
		return ObservationRecord{}, false, err
	}
	if err := s.recordState(run.id, observedAt, occurrence.ScheduledAt); err != nil {
		return record, false, err
	}
	if created {
		deliverCtx := context.Background()
		if ctx != nil {
			deliverCtx = context.WithoutCancel(ctx)
		}
		s.kernel.submitDelivery(deliverCtx, record)
	}
	return record, created, nil
}

func (s *scheduleSourceRuntime) recordState(id string, evaluatedAt, emittedAt time.Time) error {
	if s.store == nil {
		return nil
	}
	if evaluatedAt.IsZero() {
		evaluatedAt = s.kernel.now()
	}
	return s.store.RecordScheduleState(ScheduleStateRecord{
		ObservableID:           id,
		LastEvaluatedAt:        evaluatedAt.UTC(),
		LastEmittedScheduledAt: emittedAt.UTC(),
		UpdatedAt:              s.kernel.now(),
	})
}

func (s *scheduleSourceRuntime) recordPausedState(id string, pausedAt time.Time) error {
	if s.store == nil {
		return nil
	}
	if pausedAt.IsZero() {
		pausedAt = s.kernel.now()
	}
	var emittedAt time.Time
	if state, ok, err := s.store.ScheduleState(id); err != nil {
		return err
	} else if ok {
		emittedAt = state.LastEmittedScheduledAt
	}
	return s.store.RecordScheduleState(ScheduleStateRecord{
		ObservableID:           id,
		Paused:                 true,
		LastEvaluatedAt:        pausedAt.UTC(),
		LastEmittedScheduledAt: emittedAt.UTC(),
		UpdatedAt:              s.kernel.now(),
	})
}

func shouldPreserveIntervalStartupBaseline(spec scheduleRuntimeSpec, state ScheduleStateRecord) bool {
	return spec.Interval != nil && !state.LastEvaluatedAt.IsZero() && state.LastEmittedScheduledAt.IsZero()
}

func stopScheduleTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}
