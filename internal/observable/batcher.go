package observable

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/eventmedia"
)

type BatcherOptions struct {
	RunID         string
	WorkDir       string
	MaxEventBytes int64
}

type Batcher struct {
	spec          commandRuntimeSpec
	store         *Store
	runID         string
	workDir       string
	maxEventBytes int64
	batch         *activeBatch
}

type activeBatch struct {
	streams          []string
	contents         []string
	attachments      []eventmedia.AttachmentRef
	attachmentBytes  int64
	attachmentErrors []string
	kind             string
	severity         string
	windowStart      time.Time
	windowEnd        time.Time
}

func NewBatcher(spec Spec, store *Store, opts BatcherOptions) (*Batcher, error) {
	runtimeSpec, ok := spec.commandRuntime()
	if !ok {
		return nil, fmt.Errorf("observable %q is not a command source", spec.ID)
	}
	return newCommandBatcher(runtimeSpec, store, opts), nil
}

func newCommandBatcher(spec commandRuntimeSpec, store *Store, opts BatcherOptions) *Batcher {
	maxEventBytes := opts.MaxEventBytes
	if maxEventBytes <= 0 {
		maxEventBytes = eventmedia.DefaultMaxEventBytes
	}
	return &Batcher{
		spec:          spec,
		store:         store,
		runID:         opts.RunID,
		workDir:       opts.WorkDir,
		maxEventBytes: maxEventBytes,
	}
}

func (b *Batcher) Add(unit ParsedUnit) ([]ObservationRecord, error) {
	if b == nil || strings.TrimSpace(unit.Content) == "" {
		return nil, nil
	}
	if unit.ReceivedAt.IsZero() {
		unit.ReceivedAt = time.Now().UTC()
	}
	startsNewBatch := b.batch != nil && !b.batch.windowStart.IsZero() && unit.ReceivedAt.Sub(b.batch.windowStart) >= time.Duration(b.spec.Batch.IntervalSeconds)*time.Second
	remainingEventBytes := b.maxEventBytes
	if b.batch != nil && !startsNewBatch {
		remainingEventBytes -= b.batch.attachmentBytes
	}
	var snapshot attachmentSnapshot
	if len(unit.Attachments) > 0 && remainingEventBytes <= 0 {
		unit.AttachmentErrors = append(unit.AttachmentErrors, attachmentBudgetError(b.maxEventBytes, 0))
	} else {
		snapshot = snapshotAttachmentRefs(b.workDir, unit.Attachments, remainingEventBytes)
		if snapshot.eventBytesExceeded {
			unit.AttachmentErrors = append(unit.AttachmentErrors, attachmentBudgetError(b.maxEventBytes, remainingEventBytes))
		} else {
			unit.AttachmentErrors = append(unit.AttachmentErrors, snapshot.errors...)
		}
	}
	unit.Attachments = snapshot.refs

	var emitted []ObservationRecord
	if startsNewBatch {
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
	} else {
		b.batch.severity = maxSeverity(b.batch.severity, unit.Severity)
	}
	b.batch.attachmentBytes += snapshot.bytes
	b.batch.streams = append(b.batch.streams, unit.Stream)
	b.batch.contents = append(b.batch.contents, unit.Content)
	b.batch.attachments = append(b.batch.attachments, unit.Attachments...)
	b.batch.attachmentErrors = append(b.batch.attachmentErrors, unit.AttachmentErrors...)
	b.batch.windowEnd = unit.ReceivedAt
	return emitted, nil
}

func attachmentBudgetError(limit, remaining int64) string {
	return fmt.Sprintf("event attachments exceed %d bytes (batch remaining: %d bytes)", limit, max(remaining, 0))
}

func (b *Batcher) FlushDue(now time.Time, reason string) ([]ObservationRecord, error) {
	if b == nil || b.batch == nil || b.batch.windowStart.IsZero() {
		return nil, nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if now.Sub(b.batch.windowStart) < time.Duration(b.spec.Batch.IntervalSeconds)*time.Second {
		return nil, nil
	}
	return b.Flush(reason)
}

func (b *Batcher) Flush(reason string) ([]ObservationRecord, error) {
	_ = reason
	if b == nil || b.batch == nil || len(b.batch.contents) == 0 {
		return nil, nil
	}
	current := b.batch
	full := strings.Join(current.contents, "\n")
	originalChars := len([]rune(full))
	record := ObservationRecord{
		ObservableID:     b.spec.ID,
		RunID:            b.runID,
		Kind:             resolvedKind(current.kind),
		Severity:         resolvedSeverity(current.severity),
		Stream:           mergedStream(current.streams),
		WindowStart:      current.windowStart,
		WindowEnd:        current.windowEnd,
		Content:          full,
		Attachments:      append([]eventmedia.AttachmentRef(nil), current.attachments...),
		AttachmentErrors: append([]string(nil), current.attachmentErrors...),
		OriginalChars:    originalChars,
		State:            ObservationStateRecorded,
	}
	if len(record.AttachmentErrors) > 0 {
		record.AttachmentState = ObservationAttachmentStateError
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
	b.batch = nil
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

func maxSeverity(current, next string) string {
	if severityRank(resolvedSeverity(next)) > severityRank(resolvedSeverity(current)) {
		return resolvedSeverity(next)
	}
	return resolvedSeverity(current)
}

func severityRank(severity string) int {
	switch resolvedSeverity(severity) {
	case "critical":
		return 4
	case "error":
		return 3
	case "warning":
		return 2
	default:
		return 1
	}
}
