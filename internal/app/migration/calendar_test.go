package migration

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

const sourceCalendarJSON = `{"version":1,"entries":{"reminder":{"id":"reminder","name":"Lunar reminder","content":"Review project","frequency":"yearly","timezone":"Asia/Shanghai","months":[8],"days":[5],"times":["21:00"],"lunar":{"leap_month":"regular"},"enabled":true,"state":{"last_evaluated_at":"2026-09-15T13:02:44.04728Z"},"next_at":"2027-09-05T13:00:00Z"}}}`

func calendarConversionFixture() (application.Scope, legacy.Fleet, CalendarBindings) {
	owner := application.Scope{Access: application.Access{ActorID: uuid.NewString(), TenantID: uuid.NewString(), UserID: uuid.NewString()}, FleetID: uuid.NewString(), ActorEpoch: 1, MemberEpoch: 1}
	scope := owner
	scope.AgentID, scope.AgentEpoch = uuid.NewString(), 1
	data := []byte(sourceCalendarJSON)
	file := legacy.SourceFile{Path: "extensions/calendar/calendar.json", Data: data, Size: int64(len(data)), SHA256: configDigest(data)}
	source := legacy.Fleet{ID: "source-fleet", Agents: []legacy.Agent{{Definition: legacy.AgentDefinition{ID: "2zvqer"}, Files: []legacy.SourceFile{file}}}}
	bindings := CalendarBindings{SourceSHA256: strings.Repeat("a", 64), CapturedAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), Agents: map[string]application.Scope{"2zvqer": scope}}
	return owner, source, bindings
}

func TestConvertCalendarKeepsCapturedLunarClockAndNoneDefault(t *testing.T) {
	owner, source, bindings := calendarConversionFixture()
	before := bytes.Clone(source.Agents[0].Files[0].Data)
	result, err := ConvertCalendar(owner, source, bindings)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Import.Schedules) != 1 {
		t.Fatal(result)
	}
	item := result.Import.Schedules[0]
	if item.CatchUp != "none" || item.Mode != "main" || item.AgentID != bindings.Agents["2zvqer"].AgentID || item.Clock.LastEvaluatedAt.Format(time.RFC3339Nano) != "2026-09-15T13:02:44.04728Z" || item.NextAt.Format(time.RFC3339) != "2027-09-05T13:00:00Z" || result.Schedules["2zvqer"]["reminder"] != item.ID {
		t.Fatal(item, result.Schedules)
	}
	again, err := ConvertCalendar(owner, source, bindings)
	a, _ := json.Marshal(result)
	b, _ := json.Marshal(again)
	if err != nil || !bytes.Equal(a, b) || !bytes.Equal(before, source.Agents[0].Files[0].Data) {
		t.Fatal("conversion changed source or retry", err)
	}
	// Different source Agents may legitimately use the same schedule name/ID.
	second := source.Agents[0]
	second.Definition.ID = "4dnht5"
	source.Agents = append(source.Agents, second)
	scope := bindings.Agents["2zvqer"]
	scope.AgentID = uuid.NewString()
	bindings.Agents["4dnht5"] = scope
	multiple, err := ConvertCalendar(owner, source, bindings)
	if err != nil || len(multiple.Import.Schedules) != 2 || multiple.Schedules["2zvqer"]["reminder"] == multiple.Schedules["4dnht5"]["reminder"] {
		t.Fatal(multiple, err)
	}
}

func TestConvertCalendarRejectsUnprovenSourceState(t *testing.T) {
	for _, name := range []string{"hash", "duplicate-file", "unknown", "trailing", "version", "identity", "next", "interval", "pending", "attachment", "sent", "error", "scope", "extra-binding", "duplicate-target", "latest-window", "long-content"} {
		t.Run(name, func(t *testing.T) {
			owner, source, bindings := calendarConversionFixture()
			file := &source.Agents[0].Files[0]
			var doc map[string]any
			if err := json.Unmarshal(file.Data, &doc); err != nil {
				t.Fatal(err)
			}
			entry := doc["entries"].(map[string]any)["reminder"].(map[string]any)
			switch name {
			case "latest-window":
				entry["catch_up"] = map[string]any{"mode": "latest"}
			case "long-content":
				entry["content"] = strings.Repeat("字", 1001)
			case "unknown":
				entry["ignored_policy"] = true
			case "version":
				doc["version"] = 2
			case "identity":
				entry["id"] = "different"
			case "next":
				entry["next_at"] = "2027-09-05T13:01:00Z"
			case "interval":
				entry["frequency"] = "interval"
				entry["every_seconds"] = 60
			case "pending":
				entry["pending"] = map[string]any{"event_id": "pending-effect"}
			case "attachment":
				entry["attachments"] = []any{map[string]any{"path": "/private/media"}}
			case "sent":
				entry["last_sent_at"] = "2026-09-15T13:00:00Z"
			case "error":
				entry["scheduling_error"] = "unknown recurrence"
			case "scope":
				scope := bindings.Agents["2zvqer"]
				scope.FleetID = uuid.NewString()
				bindings.Agents["2zvqer"] = scope
			case "extra-binding":
				bindings.Agents["4dnht5"] = bindings.Agents["2zvqer"]
			case "duplicate-target":
				source.Agents = append(source.Agents, legacy.Agent{Definition: legacy.AgentDefinition{ID: "4dnht5"}})
				bindings.Agents["4dnht5"] = bindings.Agents["2zvqer"]
			}
			file.Data, _ = json.Marshal(doc)
			if name == "trailing" {
				file.Data = append(file.Data, []byte(" {}")...)
			}
			file.Size, file.SHA256 = int64(len(file.Data)), configDigest(file.Data)
			if name == "hash" {
				file.SHA256 = strings.Repeat("f", 64)
			}
			if name == "duplicate-file" {
				source.Agents[0].Files = append(source.Agents[0].Files, *file)
			}
			if _, err := ConvertCalendar(owner, source, bindings); err == nil {
				t.Fatal("unproven Calendar state accepted", name)
			}
		})
	}
}

func TestConvertCalendarRetainsSkippedOnceAsCompleted(t *testing.T) {
	owner, source, bindings := calendarConversionFixture()
	file := &source.Agents[0].Files[0]
	file.Data = []byte(`{"version":1,"entries":{"missed":{"id":"missed","content":"No replay","frequency":"once","at":"2026-09-01T01:00:00Z","enabled":true,"state":{"last_evaluated_at":"2026-09-01T02:00:00Z"}}}}`)
	file.Size, file.SHA256 = int64(len(file.Data)), configDigest(file.Data)
	result, err := ConvertCalendar(owner, source, bindings)
	if err != nil {
		t.Fatal(err)
	}
	state, err := result.Import.BuildState(owner)
	if err != nil {
		t.Fatal(err)
	}
	id := result.Schedules["2zvqer"]["missed"]
	job := state.Jobs[id]
	if job == nil || job.Name != "missed" || job.Status != "completed" || !job.NextAt.IsZero() || len(state.Deliveries) != 0 {
		t.Fatal(job, state.Deliveries)
	}
	if err := state.Recover(id, bindings.CapturedAt); err != nil {
		t.Fatal(err)
	}
	if err := state.Advance(id, bindings.CapturedAt.Add(time.Hour)); err != nil || len(state.Deliveries) != 0 {
		t.Fatal("completed source replayed", state.Deliveries, err)
	}
}
