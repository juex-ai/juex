package observable

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

type BatcherOptions struct {
	RunID string
}

type Batcher struct {
	spec  Spec
	store *Store
	runID string
	batch *activeBatch
}

type activeBatch struct {
	streams     []string
	contents    []string
	kind        string
	severity    string
	windowStart time.Time
	windowEnd   time.Time
}

func NewBatcher(spec Spec, store *Store, opts BatcherOptions) *Batcher {
	return &Batcher{spec: spec, store: store, runID: opts.RunID}
}

func (b *Batcher) Add(unit ParsedUnit) ([]ObservationRecord, error) {
	if b == nil || strings.TrimSpace(unit.Content) == "" {
		return nil, nil
	}
	if unit.ReceivedAt.IsZero() {
		unit.ReceivedAt = time.Now().UTC()
	}
	var emitted []ObservationRecord
	if b.batch != nil && !b.batch.windowStart.IsZero() && unit.ReceivedAt.Sub(b.batch.windowStart) >= time.Duration(b.spec.Batch.IntervalSeconds)*time.Second {
		flushed, err := b.Flush("interval")
		if err != nil {
			return nil, err
		}
		emitted = append(emitted, flushed...)
	}
	if b.batch == nil {
		b.batch = &activeBatch{
			kind:        resolvedKind(unit.Kind),
			severity:    resolvedSeverity(unit.Severity),
			windowStart: unit.ReceivedAt,
			windowEnd:   unit.ReceivedAt,
		}
	}
	b.batch.streams = append(b.batch.streams, unit.Stream)
	b.batch.contents = append(b.batch.contents, unit.Content)
	b.batch.windowEnd = unit.ReceivedAt
	return emitted, nil
}

func (b *Batcher) Flush(reason string) ([]ObservationRecord, error) {
	_ = reason
	if b == nil || b.batch == nil || len(b.batch.contents) == 0 {
		return nil, nil
	}
	current := b.batch
	b.batch = nil
	full := strings.Join(current.contents, "\n")
	originalChars := len([]rune(full))
	record := ObservationRecord{
		ObservableID:  b.spec.ID,
		RunID:         b.runID,
		Kind:          resolvedKind(current.kind),
		Severity:      resolvedSeverity(current.severity),
		Stream:        mergedStream(current.streams),
		WindowStart:   current.windowStart,
		WindowEnd:     current.windowEnd,
		Content:       full,
		OriginalChars: originalChars,
		State:         ObservationStateRecorded,
	}
	record.ID = BuildObservationID(record)
	if originalChars > b.spec.Batch.MaxChars {
		record.Truncated = true
		record.Content = previewContent(full, b.spec.Batch.MaxChars)
		record.ArtifactPath = b.store.ArtifactPath(b.spec.ID, record.ID)
		if err := os.MkdirAll(filepath.Dir(record.ArtifactPath), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(record.ArtifactPath, []byte(full), 0o644); err != nil {
			return nil, err
		}
	}
	persisted, err := b.store.RecordObservation(record)
	if err != nil {
		return nil, err
	}
	return []ObservationRecord{persisted}, nil
}

func mergedStream(streams []string) string {
	seen := map[string]struct{}{}
	var out []string
	for _, stream := range streams {
		if stream == "" {
			continue
		}
		if _, ok := seen[stream]; ok {
			continue
		}
		seen[stream] = struct{}{}
		out = append(out, stream)
	}
	return strings.Join(out, "+")
}

func previewContent(content string, max int) string {
	runes := []rune(content)
	if max <= 0 || len(runes) <= max {
		return content
	}
	if max < 32 {
		return string(runes[:max])
	}
	marker := []rune("\n...[truncated]...\n")
	available := max - len(marker)
	if available <= 0 {
		return string(runes[:max])
	}
	head := available / 2
	tail := available - head
	return string(runes[:head]) + string(marker) + string(runes[len(runes)-tail:])
}
