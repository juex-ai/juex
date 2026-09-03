package thread

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

var ErrInvalidMetadata = errors.New("thread: invalid metadata")

func readProjectionFile(dir, id string) (Projection, error) {
	path := filepath.Join(dir, projectionFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Projection{}, err
		}
		return Projection{}, fmt.Errorf("thread: read metadata %s: %w", id, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var projection Projection
	if err := decoder.Decode(&projection); err != nil {
		return Projection{}, fmt.Errorf("%w for %s: %v", ErrInvalidMetadata, id, err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return Projection{}, fmt.Errorf("%w for %s: %v", ErrInvalidMetadata, id, err)
	}
	if err := validateProjectionMetadata(projection, id); err != nil {
		return Projection{}, err
	}
	return projection, nil
}

func validateProjectionMetadata(projection Projection, id string) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w for %s: %s", ErrInvalidMetadata, id, fmt.Sprintf(format, args...))
	}
	if projection.Version != ProjectionVersion {
		return fail("unsupported version %d", projection.Version)
	}
	if projection.ThreadID != id || !ValidID(id) {
		return fail("thread_id %q does not match directory", projection.ThreadID)
	}
	if projection.Alias == "" {
		return fail("alias is empty")
	}
	if id == MainID {
		if projection.Alias != MainAlias || projection.ParentThreadID != "" {
			return fail("invalid Main identity")
		}
	} else if !ValidID(projection.ParentThreadID) || projection.ParentThreadID == id {
		return fail("invalid parent_thread_id %q", projection.ParentThreadID)
	}
	if projection.CreatedAt.IsZero() || projection.UpdatedAt.IsZero() || projection.LastActivityAt.IsZero() {
		return fail("timestamps must be set")
	}
	if projection.UpdatedAt.Before(projection.CreatedAt.Time) || projection.LastActivityAt.Before(projection.CreatedAt.Time) ||
		projection.UpdatedAt.Before(projection.LastActivityAt.Time) {
		return fail("timestamps are out of order")
	}
	if projection.Revision == 0 {
		return fail("revision is zero")
	}
	if err := validateProjectionLifecycle(projection); err != nil {
		return fail("%v", err)
	}
	if id == MainID && projection.RetentionState != RetentionActive {
		return fail("Main cannot be archived")
	}
	if projection.ArchivedAt != nil && (projection.ArchivedAt.Before(projection.CreatedAt.Time) || projection.UpdatedAt.Before(projection.ArchivedAt.Time)) {
		return fail("archived_at is out of order")
	}
	if len(projection.Generations) == 0 || projection.Counts.GenerationCount != len(projection.Generations) {
		return fail("generation registry count mismatch")
	}
	for index, generation := range projection.Generations {
		wantOrdinal := index + 1
		if generation.ID != generationID(wantOrdinal) || generation.Ordinal != wantOrdinal || generation.StartSeq == 0 || generation.StartOffset < 0 {
			return fail("invalid Generation registry entry %d", index)
		}
		if index > 0 {
			previous := projection.Generations[index-1]
			if generation.StartSeq <= previous.StartSeq || generation.StartOffset <= previous.StartOffset {
				return fail("Generation registry is not ordered at entry %d", index)
			}
		}
	}
	if projection.CurrentGeneration != projection.Generations[len(projection.Generations)-1] {
		return fail("current Generation is not the final registry entry")
	}
	if projection.Counts.TurnCount < 0 || projection.Counts.PendingInputCount < 0 {
		return fail("negative counters")
	}
	journal := projection.Journal
	if journal.ProjectedSeq == 0 || journal.ProjectedOffset <= 0 {
		return fail("Journal cursor is empty")
	}
	if (journal.LastCheckpointSeq == 0) != (journal.LastCheckpointOffset == 0) ||
		journal.LastCheckpointSeq > journal.ProjectedSeq ||
		journal.LastCheckpointOffset < 0 || journal.LastCheckpointOffset >= journal.ProjectedOffset {
		return fail("checkpoint cursor is inconsistent")
	}
	for index, generation := range projection.Generations {
		if generation.StartSeq > journal.ProjectedSeq || generation.StartOffset >= journal.ProjectedOffset {
			return fail("Generation registry entry %d is beyond the Journal cursor", index)
		}
	}
	return nil
}

// applyAuthoritativeProjection overlays the durable Thread metadata after the
// Journal has restored runtime-only state. Fields materialized from Journal
// commits must still agree so a committed Journal append whose metadata write
// failed is surfaced instead of silently accepting a stale thread.json.
func applyAuthoritativeProjection(state *ReplayState, metadata Projection) error {
	if state == nil {
		return fmt.Errorf("%w for %s: nil replay state", ErrInvalidMetadata, metadata.ThreadID)
	}
	replayed := state.Projection
	if !replayed.CreatedAt.Equal(metadata.CreatedAt.Time) ||
		!replayed.LastActivityAt.Equal(metadata.LastActivityAt.Time) ||
		replayed.CurrentGeneration != metadata.CurrentGeneration ||
		replayed.Counts != metadata.Counts ||
		replayed.TokenUsage != metadata.TokenUsage ||
		!reflect.DeepEqual(replayed.ContextUsage, metadata.ContextUsage) ||
		replayed.Journal != metadata.Journal {
		return fmt.Errorf("%w for %s: metadata does not match Journal materialization", ErrInvalidMetadata, metadata.ThreadID)
	}
	state.Projection = cloneProjection(metadata)
	return nil
}
