package managedruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

type ObservationAttachment struct {
	Path       string `json:"path,omitempty"`
	Name       string `json:"name,omitempty"`
	MediaType  string `json:"media_type,omitempty"`
	ArtifactID string `json:"artifact_id,omitempty"`
	TransferID string `json:"transfer_id,omitempty"`
	Error      string `json:"error,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	Ordinal    int    `json:"ordinal,omitempty"`
}

type CommandObservationUnit struct {
	Content     string                  `json:"content"`
	Kind        string                  `json:"kind"`
	Severity    string                  `json:"severity"`
	Stream      string                  `json:"stream"`
	Time        time.Time               `json:"time"`
	ReceivedAt  time.Time               `json:"received_at"`
	Ordinal     int                     `json:"ordinal,omitempty"`
	Attachments []ObservationAttachment `json:"attachments,omitempty"`
}

// Pending batches commit with the raw cursor before Execution output is acked.
// Limits cause an early flush; full content remains readable by observation ID.
type CommandObservationBatch struct {
	Units    []CommandObservationUnit `json:"units,omitempty"`
	Deadline time.Time                `json:"deadline,omitempty"`
	Offset   int64                    `json:"offset,omitempty"`
	Bytes    int                      `json:"bytes,omitempty"`
	Ordinal  int                      `json:"ordinal,omitempty"`
}

func parseCommandObservation(source ObservationSource, operation ToolOperation) (ObservationBatch, error) {
	snapshot := operation.Snapshot
	batch := ObservationBatch{Cursor: snapshot.NextCursor, More: snapshot.NextCursor < snapshot.OutputBytes, Discarding: source.Discarding, Command: source.Command}
	// The caller may retry the same source on a transient attachment failure.
	batch.Command.Units = append([]CommandObservationUnit(nil), source.Command.Units...)
	emit := func(kind string, offset int64, value any) {
		encoded, _ := json.Marshal(value)
		batch.Facts = append(batch.Facts, Observation{ID: observationID(source, kind, offset), Kind: kind, EnvironmentID: source.EnvironmentID, OperationID: source.OperationID, Offset: offset, Data: encoded, CreatedAt: time.Now().UTC()})
	}
	flush := func() {
		if len(batch.Command.Units) == 0 {
			return
		}
		var content []string
		var attachments []ObservationAttachment
		severity := "info"
		for _, unit := range batch.Command.Units {
			content = append(content, unit.Content)
			attachments = append(attachments, unit.Attachments...)
			if severityRank(unit.Severity) > severityRank(severity) {
				severity = unit.Severity
			}
		}
		full := strings.Join(content, "\n")
		limit := source.Options.Batch.MaxChars
		if limit == 0 {
			limit = 1000
		}
		preview := []rune(full)
		truncated := len(preview) > limit
		if truncated {
			preview = preview[:limit]
		}
		emit("command.observation", batch.Command.Offset, map[string]any{"kind": batch.Command.Units[0].Kind, "severity": severity, "content": string(preview), "full_content": full, "truncated": truncated, "units": batch.Command.Units, "attachments": attachments})
		batch.Facts[len(batch.Facts)-1].ID = observationID(source, fmt.Sprintf("command.observation/%d", batch.Command.Ordinal), batch.Command.Offset)
		batch.Command = CommandObservationBatch{}
	}
	invalid := func(offset int64, message string) {
		emit("command.invalid_observation", offset, map[string]string{"error": message})
	}
	if snapshot.OutputExpired {
		flush()
		emit("operation.output_expired", source.Cursor, map[string]string{"error": "source output expired before observation"})
		batch.Closed = true
		batch.More = false
		return batch, nil
	}
	data := append(bytes.Clone(source.Pending), snapshot.Output...)
	base := source.Cursor - int64(len(source.Pending))
	for len(data) > 0 {
		if len(batch.Facts) >= 128 {
			batch.Cursor = base
			batch.More = true
			break
		}
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			if !batch.Discarding && len(data) > 512<<10 {
				invalid(base, "observation envelope exceeds 512 KiB")
				batch.Discarding = true
			}
			if !batch.Discarding {
				batch.Pending = bytes.Clone(data)
			}
			break
		}
		next := base + int64(end+1)
		if batch.Discarding {
			batch.Discarding = false
		} else if end > 512<<10 {
			invalid(next, "observation envelope exceeds 512 KiB")
		} else {
			units, err := commandUnits(source.Options, data[:end], next)
			if err != nil {
				invalid(next, err.Error())
			} else {
				for _, unit := range units {
					size, _ := json.Marshal(unit)
					attachments := len(unit.Attachments)
					for _, previous := range batch.Command.Units {
						attachments += len(previous.Attachments)
					}
					if len(batch.Command.Units) > 0 && (attachments > 16 || len(batch.Command.Units) >= 32 || batch.Command.Bytes+len(size) > 128<<10 || !unit.ReceivedAt.Before(batch.Command.Deadline)) {
						flush()
					}
					if len(batch.Command.Units) == 0 {
						interval := source.Options.Batch.IntervalSeconds
						if interval == 0 {
							interval = 5
						}
						received := unit.ReceivedAt
						if now := time.Now(); received.After(now) {
							received = now
						}
						batch.Command.Deadline = received.Add(time.Duration(interval) * time.Second)
					}
					batch.Command.Units = append(batch.Command.Units, unit)
					batch.Command.Offset = next
					batch.Command.Ordinal = unit.Ordinal
					batch.Command.Bytes += len(size)
				}
			}
		}
		data, base = data[end+1:], next
	}
	terminal := execprotocol.State(operation.State).Terminal() && !batch.More
	if terminal || !batch.More && !batch.Command.Deadline.IsZero() && !time.Now().Before(batch.Command.Deadline) {
		flush()
	}
	if terminal {
		if len(batch.Pending) > 0 {
			invalid(batch.Cursor, "process ended with an incomplete observation envelope")
		}
		batch.Pending = nil
		batch.Discarding = false
		batch.Closed = true
		failed := operation.State != "completed" || snapshot.ExitCode != nil && *snapshot.ExitCode != 0
		notify := source.Options.OnExit == "always" || source.Options.OnExit == "nonzero" && failed
		if notify {
			emit("command.exit", batch.Cursor, map[string]any{"state": operation.State, "error": snapshot.Error, "exit_code": snapshot.ExitCode})
		}
		emit("operation.terminal", batch.Cursor, map[string]any{"state": operation.State, "error": snapshot.Error, "exit_code": snapshot.ExitCode, "truncated": snapshot.Truncated})
	} else if len(batch.Command.Units) > 0 {
		batch.RetryAfter = max(time.Millisecond, time.Until(batch.Command.Deadline))
	}
	return batch, nil
}

func commandUnits(options execprotocol.ObservableOptions, line []byte, offset int64) ([]CommandObservationUnit, error) {
	var envelope struct {
		Type   string    `json:"type"`
		Stream string    `json:"stream"`
		Text   string    `json:"text"`
		Time   time.Time `json:"time"`
	}
	if json.Unmarshal(line, &envelope) != nil || envelope.Type != "observation" || envelope.Stream != "stdout" && envelope.Stream != "stderr" || envelope.Time.IsZero() {
		return nil, fmt.Errorf("invalid command observation envelope")
	}
	unit := CommandObservationUnit{Content: strings.TrimRight(envelope.Text, "\r\n"), Kind: options.Kind, Severity: options.Severity, Stream: envelope.Stream, Time: envelope.Time, ReceivedAt: envelope.Time}
	if unit.Kind == "" {
		unit.Kind = "log_batch"
	}
	if unit.Severity == "" {
		unit.Severity = "info"
	}
	if strings.TrimSpace(unit.Content) == "" {
		return nil, nil
	}
	if options.Parser.Type == "jsonl" {
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(unit.Content), &object) != nil || object == nil {
			return nil, fmt.Errorf("invalid observable JSONL")
		}
		read := func(key string) string { var value string; _ = json.Unmarshal(object[key], &value); return value }
		if value, ok := object[options.Parser.ContentField]; ok && options.Parser.ContentField != "" {
			var text string
			if json.Unmarshal(value, &text) == nil {
				unit.Content = text
			} else {
				unit.Content = string(value)
			}
		}
		if value := read(options.Parser.KindField); value != "" {
			unit.Kind = value
		}
		if value := read(options.Parser.SeverityField); severityRank(value) > 0 {
			unit.Severity = value
		}
		if value := read(options.Parser.TimeField); value != "" {
			when, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return nil, fmt.Errorf("invalid observation time")
			}
			unit.Time = when
		}
		if value, ok := object[options.Parser.AttachmentsField]; ok && options.Parser.AttachmentsField != "" {
			if json.Unmarshal(value, &unit.Attachments) != nil || len(unit.Attachments) > 16 {
				return nil, fmt.Errorf("attachments must be at most 16 path objects")
			}
			for i := range unit.Attachments {
				a := &unit.Attachments[i]
				if a.Path == "" || len(a.Path) > 4096 || strings.ContainsRune(a.Path, 0) || len(a.Name) > 256 || len(a.MediaType) > 256 || a.ArtifactID != "" || a.TransferID != "" || a.Error != "" || a.Offset != 0 || a.Ordinal != 0 {
					return nil, fmt.Errorf("invalid observation attachment")
				}
				a.Offset = offset
				a.Ordinal = i
			}
		}
	}
	if len(options.Filters) == 0 {
		return []CommandObservationUnit{unit}, nil
	}
	var result []CommandObservationUnit
	for index, filter := range options.Filters {
		matched := filter.Contains != "" && strings.Contains(unit.Content, filter.Contains)
		if filter.Regex != "" {
			regex, err := regexp.Compile(filter.Regex)
			if err != nil {
				return nil, err
			}
			matched = regex.MatchString(unit.Content)
		}
		if matched {
			next := unit
			next.Ordinal = index
			if filter.Kind != "" {
				next.Kind = filter.Kind
			}
			if filter.Severity != "" {
				next.Severity = filter.Severity
			}
			result = append(result, next)
		}
	}
	return result, nil
}
func severityRank(value string) int {
	switch value {
	case "critical":
		return 4
	case "error":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 0
}
