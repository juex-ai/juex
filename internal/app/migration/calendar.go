package migration

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/calendar"
	"github.com/juex-ai/juex/internal/calendar/recurrence"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/migration/legacy"
)

type CalendarBindings struct {
	SourceSHA256 string
	CapturedAt   time.Time
	Agents       map[string]application.Scope
}

type CalendarConversion struct {
	Import    calendar.FleetImport         `json:"import"`
	Schedules map[string]map[string]string `json:"schedules"`
}

// Calendar v1 is the captured extension format. Unknown fields must not become
// silently discarded behavior when the standalone scheduler is retired.
type sourceCalendarEntry struct {
	recurrence.Rule
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Content string `json:"content"`
	CatchUp struct {
		Mode               string `json:"mode,omitempty"`
		MaxLatenessMinutes int    `json:"max_lateness_minutes,omitempty"`
	} `json:"catch_up,omitempty"`
	Attachments []json.RawMessage `json:"attachments,omitempty"`
	Enabled     bool              `json:"enabled"`
	State       struct {
		LastEvaluatedAt        time.Time `json:"last_evaluated_at"`
		LastEmittedScheduledAt time.Time `json:"last_emitted_scheduled_at,omitempty"`
	} `json:"state"`
	NextAt          *time.Time      `json:"next_at,omitempty"`
	Pending         json.RawMessage `json:"pending,omitempty"`
	LastSentAt      *time.Time      `json:"last_sent_at,omitempty"`
	SchedulingError string          `json:"scheduling_error,omitempty"`
}

var sourceCalendarID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// ConvertCalendar only reads captured bytes. The caller verifies that the v1
// extension was selected for these source Agents and stops target writers before
// importing the complete Fleet. No old scheduler or recovery store is opened.
func ConvertCalendar(owner application.Scope, source legacy.Fleet, bindings CalendarBindings) (CalendarConversion, error) {
	if !owner.Valid() || owner.AgentID != "" || source.ID == "" || len(bindings.Agents) != len(source.Agents) {
		return CalendarConversion{}, errors.New("calendar conversion requires the complete source Fleet and target bindings")
	}
	result := CalendarConversion{Import: calendar.FleetImport{Source: "juex/281889e5/fleet/" + source.ID + "/calendar-v1", SourceSHA256: bindings.SourceSHA256, CapturedAt: bindings.CapturedAt}, Schedules: map[string]map[string]string{}}
	seenSource, seenTarget := map[string]bool{}, map[string]bool{}
	for _, agent := range source.Agents {
		id := agent.Definition.ID
		scope, ok := bindings.Agents[id]
		if id == "" || seenSource[id] || !ok || !scope.Valid() || scope.AgentID == "" || seenTarget[scope.AgentID] || scope.TenantID != owner.TenantID || scope.UserID != owner.UserID || scope.FleetID != owner.FleetID {
			return CalendarConversion{}, errors.New("calendar requires distinct same-owner Agent bindings")
		}
		seenSource[id], seenTarget[scope.AgentID] = true, true
		result.Schedules[id] = map[string]string{}
		var file *legacy.SourceFile
		for i := range agent.Files {
			if agent.Files[i].Path != "extensions/calendar/calendar.json" {
				continue
			}
			if file != nil {
				return CalendarConversion{}, errors.New("repeated Calendar source file")
			}
			file = &agent.Files[i]
		}
		if file == nil {
			continue
		}
		if file.Size != int64(len(file.Data)) || configDigest(file.Data) != file.SHA256 {
			return CalendarConversion{}, errors.New("calendar source size/hash mismatch")
		}
		var doc struct {
			Version int                            `json:"version"`
			Entries map[string]sourceCalendarEntry `json:"entries"`
		}
		decoder := json.NewDecoder(bytes.NewReader(file.Data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&doc); err != nil {
			return CalendarConversion{}, fmt.Errorf("source Agent %s Calendar: %w", id, err)
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF || doc.Version != 1 || doc.Entries == nil {
			return CalendarConversion{}, errors.New("unsupported Calendar source version or framing")
		}
		for _, key := range slices.Sorted(maps.Keys(doc.Entries)) {
			entry := doc.Entries[key]
			if entry.ID != key || !sourceCalendarID.MatchString(key) {
				return CalendarConversion{}, errors.New("calendar source identity mismatch")
			}
			if entry.Frequency == recurrence.FrequencyInterval {
				return CalendarConversion{}, errors.New("calendar interval recovery anchor requires a separate conversion policy")
			}
			if len(entry.Attachments) != 0 || entry.LastSentAt != nil || entry.SchedulingError != "" || len(entry.Pending) != 0 && !bytes.Equal(bytes.TrimSpace(entry.Pending), []byte("null")) {
				return CalendarConversion{}, errors.New("calendar pending delivery, attachment, sent history or scheduling error is not converted")
			}
			if entry.CatchUp.Mode == "" {
				entry.CatchUp.Mode = "none"
			}
			// Validate the source contract before target defaults can turn an
			// invalid v1 definition into a newly runnable schedule.
			entry.Content = strings.TrimSpace(entry.Content)
			if entry.Content == "" || len([]rune(entry.Content)) > 1000 || entry.CatchUp.Mode == "latest" && (entry.CatchUp.MaxLatenessMinutes < 1 || entry.CatchUp.MaxLatenessMinutes > 1440) {
				return CalendarConversion{}, errors.New("invalid v1 Calendar content or catch-up window")
			}
			name := strings.TrimSpace(entry.Name)
			if name == "" {
				name = key
			}
			targetID := uuid.NewSHA1(uuid.NameSpaceURL, []byte("calendar-import/"+owner.FleetID+"/"+source.ID+"/"+id+"/"+key)).String()
			item := calendar.ImportedSchedule{ID: targetID, Definition: calendar.Definition{Name: name, Content: entry.Content, Mode: "main", AgentID: scope.AgentID, Rule: entry.Rule, CatchUp: entry.CatchUp.Mode, MaxLatenessMinutes: entry.CatchUp.MaxLatenessMinutes}, Scope: scope, Enabled: entry.Enabled, Clock: recurrence.State{LastEvaluatedAt: entry.State.LastEvaluatedAt, LastEmittedScheduledAt: entry.State.LastEmittedScheduledAt}}
			if entry.NextAt != nil {
				item.NextAt = *entry.NextAt
			}
			result.Import.Schedules = append(result.Import.Schedules, item)
			result.Schedules[id][key] = targetID
		}
	}
	if _, err := result.Import.BuildState(owner); err != nil {
		return CalendarConversion{}, fmt.Errorf("converted Calendar state: %w", err)
	}
	return result, nil
}
