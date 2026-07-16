package observable

import (
	"context"
	"fmt"
)

type commandSourceRuntime struct {
	spec   commandRuntimeSpec
	kernel sourceKernel
	opts   ManagerOptions
	store  *Store
}

type commandRunState struct {
	runner *runner
}

func (s *commandSourceRuntime) start(callCtx context.Context, run *observableRun) error {
	startupCtx, cancelStartup := linkedStartupContext(callCtx, run.ctx)
	defer cancelStartup()
	r := newRunner(runnerOptions{
		spec:          s.spec,
		runID:         run.runID,
		workDir:       s.opts.WorkDir,
		sandboxPolicy: s.opts.Sandbox,
		sandboxRunner: s.opts.SandboxRunner,
		store:         s.store,
		submit:        s.kernel.submitDelivery,
	})
	run.sourceState = &commandRunState{runner: r}
	cmd, err := r.start(startupCtx, run.ctx)
	if err != nil {
		run.closeQuiesced()
		_, _ = s.stop(context.Background(), run, sourceStopFailedStartup)
		_, finishErr := s.kernel.finishRun(run, terminalOutcome{State: RunStateErrored, Err: err})
		if finishErr != nil {
			s.kernel.reportWorkerError(run, finishErr)
		}
		run.closeDone()
		if finishErr != nil {
			return fmt.Errorf("%w; record terminal state: %v", err, finishErr)
		}
		return err
	}
	status := run.state
	status.State = RunStateRunning
	status.PID = cmd.Process.Pid
	if err := s.kernel.activateRun(run, status); err != nil {
		run.cancel()
		exitCode, waitErr := r.wait()
		flushed, flushErr := r.flush("start_failed")
		for _, record := range flushed {
			s.kernel.submitDelivery(context.Background(), record)
		}
		run.closeQuiesced()
		_, _ = s.stop(context.Background(), run, sourceStopFailedStartup)
		cause := firstNonNil(err, waitErr, flushErr)
		_, finishErr := s.kernel.finishRun(run, terminalOutcome{State: RunStateErrored, ExitCode: exitCode, Err: cause})
		if finishErr != nil {
			s.kernel.reportWorkerError(run, finishErr)
		}
		run.closeDone()
		if finishErr != nil {
			return fmt.Errorf("%w; record terminal state: %v", err, finishErr)
		}
		return err
	}
	go s.wait(run, r)
	<-run.workerReady
	defer run.releaseStarted()
	return s.kernel.publishStarted(run)
}

func (s *commandSourceRuntime) wait(run *observableRun, r *runner) {
	defer run.closeDone()
	run.markWorkerReady()
	run.waitForStartedOrCancellation()
	exitCode, err := r.wait()
	flushed, flushErr := r.flush("exit")
	if flushErr != nil && err == nil {
		err = flushErr
	}
	for _, record := range flushed {
		s.kernel.submitDelivery(context.Background(), record)
	}
	run.closeQuiesced()
	finished, finishErr := s.kernel.finishRun(run, terminalOutcome{State: RunStateExited, ExitCode: exitCode, Err: err})
	if finishErr != nil {
		s.kernel.reportWorkerError(run, finishErr)
	}
	if finished {
		s.notifyOnExit(run, exitCode, err)
	}
}

func (s *commandSourceRuntime) stop(ctx context.Context, run *observableRun, reason sourceStopReason) (sourceStopResult, error) {
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
	return sourceStopResult{Quiesced: true}, nil
}

func (s *commandSourceRuntime) deleteState(context.Context, string) error { return nil }

func (s *commandSourceRuntime) statusSnapshot(status ObservableStatus) ObservableStatus {
	status.Command = s.spec.Command
	status.Args = append([]string(nil), s.spec.Args...)
	status.Streams = append([]string(nil), s.spec.Streams...)
	status.Batch = s.spec.Batch
	return status
}

func (s *commandSourceRuntime) notifyOnExit(run *observableRun, exitCode *int, err error) {
	notify := s.spec.OnExit.Notify
	if notify == "" || notify == "never" {
		return
	}
	nonzero := err != nil || (exitCode != nil && *exitCode != 0)
	if notify == "nonzero" && !nonzero {
		return
	}
	severity := "info"
	if nonzero {
		severity = "error"
	}
	when := s.kernel.now()
	content := fmt.Sprintf("observable %s exited", run.id)
	if exitCode != nil {
		content = fmt.Sprintf("%s with code %d", content, *exitCode)
	}
	if err != nil {
		content = fmt.Sprintf("%s: %s", content, err.Error())
	}
	record, created, recordErr := s.kernel.recordObservation(ObservationRecord{
		ObservableID: run.id,
		RunID:        run.runID,
		Kind:         "observable_exit",
		Severity:     severity,
		WindowStart:  when,
		WindowEnd:    when,
		Content:      content,
		State:        ObservationStateRecorded,
	})
	if recordErr == nil && created {
		s.kernel.submitDelivery(context.Background(), record)
	}
}

func firstNonNil(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
