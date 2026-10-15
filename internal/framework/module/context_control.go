package module

// ContextController is the execution seam used by a context-control contribution.
// Requests are applied by the runtime between provider iterations.
type ContextController interface {
	RequestContextTransition(ContextTransitionRequest) error
	ContextWindowState() ContextWindowState
}
type ContextWindowState struct {
	CurrentTokens int
	WindowTokens  int
	ThreadID      string
	GenerationID  string
}
