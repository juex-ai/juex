package managedruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/execprotocol"
)

func observationID(source ObservationSource, kind string, offset int64) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("juex.observation.v1/%s/%s/%s/%d", source.EnvironmentID, source.OperationID, kind, offset))).String()
}

func parseObservation(source ObservationSource, operation ToolOperation) (ObservationBatch, error) {
	snapshot := operation.Snapshot
	batch := ObservationBatch{Cursor: snapshot.NextCursor, Pending: source.Pending, Discarding: source.Discarding, More: snapshot.NextCursor < snapshot.OutputBytes}
	if snapshot.NextCursor != source.Cursor+int64(len(snapshot.Output)) || snapshot.OutputBytes < snapshot.NextCursor {
		return batch, execprotocol.ErrInvalid
	}
	emit := func(kind string, offset int64, value any) {
		data, _ := json.Marshal(value)
		batch.Facts = append(batch.Facts, Observation{ID: observationID(source, kind, offset), Kind: kind, EnvironmentID: source.EnvironmentID, OperationID: source.OperationID, Offset: offset, Data: data, CreatedAt: time.Now().UTC()})
	}
	if snapshot.OutputExpired {
		emit("operation.output_expired", source.Cursor, map[string]string{"error": "source output expired before observation"})
		batch.Closed, batch.More, batch.Pending = true, false, nil
		return batch, nil
	}
	if source.Kind == "mcp_connect" {
		data := append(bytes.Clone(source.Pending), snapshot.Output...)
		base := source.Cursor - int64(len(source.Pending))
		batch.Pending = nil
		for len(data) > 0 {
			if len(batch.Facts) >= 512 {
				batch.Cursor, batch.More = base, true
				break
			}
			end := bytes.IndexByte(data, '\n')
			if end < 0 {
				if !batch.Discarding && len(data) > execprotocol.MaxMCPEventBytes {
					emit("mcp.invalid_notification", base, map[string]string{"error": "notification record exceeds 1 MiB"})
					batch.Discarding = true
				}
				if !batch.Discarding {
					batch.Pending = bytes.Clone(data)
				}
				break
			}
			line := data[:end]
			next := base + int64(end+1)
			if batch.Discarding {
				batch.Discarding = false
			} else if end+1 > execprotocol.MaxMCPEventBytes {
				emit("mcp.invalid_notification", base, map[string]string{"error": "notification record exceeds 1 MiB"})
			} else {
				var envelope struct{ Type, Method string }
				if err := json.Unmarshal(line, &envelope); err != nil {
					emit("mcp.invalid_notification", next, map[string]string{"error": "invalid notification JSON"})
				} else if envelope.Type == "notification" && strings.HasPrefix(envelope.Method, "notifications/") {
					emit("mcp.notification", next, json.RawMessage(line))
				} else if envelope.Type != "connected" && envelope.Type != "stderr" {
					emit("mcp.invalid_notification", next, map[string]string{"error": "unsupported notification record"})
				}
			}
			data, base = data[end+1:], next
		}
	}
	if execprotocol.State(operation.State).Terminal() && !batch.More {
		if len(batch.Pending) > 0 {
			emit("mcp.invalid_notification", batch.Cursor, map[string]string{"error": "connection ended with an incomplete notification record"})
		}
		batch.Pending, batch.Discarding, batch.Closed = nil, false, true
		emit("operation.terminal", batch.Cursor, map[string]any{"state": operation.State, "error": snapshot.Error, "exit_code": snapshot.ExitCode, "truncated": snapshot.Truncated, "output_expired": snapshot.OutputExpired})
	}
	return batch, nil
}
