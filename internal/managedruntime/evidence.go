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
