package observable

const (
	EventObservableStarted    = "observable.started"
	EventObservableStopped    = "observable.stopped"
	EventObservableExited     = "observable.exited"
	EventObservableErrored    = "observable.errored"
	EventObservationRecorded  = "observation.recorded"
	EventObservationQueued    = "observation.queued"
	EventObservationDelivered = "observation.delivered"
	EventObservationDropped   = "observation.dropped"
)

type ObservableEventPayload struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	State    string `json:"state"`
	RunID    string `json:"run_id,omitempty"`
	PID      int    `json:"pid,omitempty"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

type ObservationEventPayload struct {
	Observation ObservationRecord `json:"observation"`
	Error       string            `json:"error,omitempty"`
}
