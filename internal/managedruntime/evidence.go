package managedruntime

import (
	"context"
	"time"
)

// OriginalEvidence is a bounded, authenticated input, not reconstructed model
// history. Its event generation stays stable across context compactions.
type OriginalEvidence struct {
	Text                 string
	Sequence, Generation int64
	RecordedAt           time.Time
}

type EvidenceStore interface {
	ToolEvidence(context.Context, ToolWork) (OriginalEvidence, error)
}

type EvidenceDelivery struct {
	ID       string
	Scope    Scope
	ThreadID string
	OriginalEvidence
}
type EvidenceOutbox interface {
	PendingEvidence(context.Context, int) ([]EvidenceDelivery, error)
	FinishEvidence(context.Context, string, string) error
}
type EvidenceGateway interface {
	Contribute(context.Context, EvidenceDelivery) error
}
