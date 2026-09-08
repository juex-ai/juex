package module

type ContextTransitionKind string

const (
	ContextTransitionNew     ContextTransitionKind = "new"
	ContextTransitionCompact ContextTransitionKind = "compact"
)

type ContextTransitionRequest struct {
	Kind         ContextTransitionKind
	Instructions string
}
