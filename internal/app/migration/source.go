package migration

import (
	"errors"

	"github.com/juex-ai/juex/internal/migration/legacy"
)

// VerifySource compares the guarded source with the frozen bundle. It never
// refreshes the bundle or retry identity after a mismatch. The guard must remain
// held across all owner imports, with Check at every stage boundary. This proves
// byte/absence correspondence, not operator shutdown of non-cooperating writers.
func (b *Bundle) VerifySource(guard *legacy.SourceGuard) error {
	if b == nil || guard == nil {
		return errors.New("a verified bundle and source guard are required")
	}
	current, err := guard.Capture(b.source.DefaultHome.Directory)
	if err != nil {
		return err
	}
	_, expected, err := legacy.EncodeCapture(b.source)
	if err != nil {
		return err
	}
	_, actual, err := legacy.EncodeCapture(current)
	if err != nil {
		return err
	}
	if expected != actual {
		return errors.New("guarded source differs from the frozen bundle; no import is permitted")
	}
	return nil
}
