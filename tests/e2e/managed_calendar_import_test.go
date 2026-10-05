//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/app/managed"
	"github.com/juex-ai/juex/internal/calendar"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
	"github.com/juex-ai/juex/internal/calendar/recurrence"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	runtimepg "github.com/juex-ai/juex/internal/managedruntime/postgres"
)

func TestManagedCalendarImportedScheduleRunsMainWithFreshAuthority(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "current", true: "revoked"}[stale], func(t *testing.T) {
			var calls atomic.Int32
			f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil || !strings.Contains(string(body), "Imported reminder marker-37") {
					t.Error("imported instruction missing from Main", err)
				}
				calls.Add(1)
				streamManagedReply(w, "Imported reminder handled")
			})
			ctx := context.Background()
			if err := calendarpg.Migrate(ctx, f.pool); err != nil {
				t.Fatal(err)
			}
			store := calendarpg.New(f.pool)
			service := &calendar.Service{Repository: store, Authority: f.authority, MainInputs: managed.CalendarMainInputs{Runtime: f.service}}
			t.Cleanup(service.CloseScheduler)
			gateway := managed.RuntimeApplications{Calendar: service}
			f.service.Applications, f.service.Triggers = gateway, gateway
			scope, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor, AgentID: f.agent.ID}, true)
			if err != nil {
				t.Fatal(err)
			}
			owner := scope
			owner.AgentID, owner.AgentEpoch = "", 0
			if stale {
				scope.AgentEpoch++
			}
			now := time.Now().UTC()
			at := now.AddDate(100, 0, 0)
			value := calendar.FleetImport{Source: "fixture/calendar-main", SourceSHA256: strings.Repeat("a", 64), CapturedAt: now, Schedules: []calendar.ImportedSchedule{{ID: uuid.NewString(), Definition: calendar.Definition{Name: "Imported reminder", Content: "Imported reminder marker-37", Mode: "main", AgentID: scope.AgentID, CatchUp: "none", Rule: recurrence.Rule{Frequency: recurrence.FrequencyOnce, At: at.Format(time.RFC3339Nano)}}, Scope: scope, Enabled: true, Clock: recurrence.State{LastEvaluatedAt: now.Add(-time.Hour)}, NextAt: at}}}
			if err := store.ImportFleet(ctx, owner, value); err != nil {
				t.Fatal(err)
			}
			if err := service.Step(ctx); err != nil {
				t.Fatal(err)
			}
			if stale {
				page, err := service.Schedules(ctx, owner.Access, 0, 50)
				if err != nil || len(page.Schedules) != 1 || page.Schedules[0].Status != "paused" || page.Schedules[0].PauseReason != "target_unavailable" {
					t.Fatal(page, err)
				}
				var inputs int
				if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs`).Scan(&inputs); err != nil || inputs != 0 || calls.Load() != 0 {
					t.Fatal("old import scope executed", inputs, calls.Load(), err)
				}
				return
			}
			// Establish the real recovery cutoff before moving this fixture's
			// due clock. An arbitrary delay after import cannot turn live work
			// into an intentionally skipped startup occurrence.
			var due time.Time
			if err := f.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&due); err != nil {
				t.Fatal(err)
			}
			if err := store.Update(ctx, scope, func(s *calendar.State) error {
				job := s.Jobs[value.Schedules[0].ID]
				job.Rule.At = due.UTC().Format(time.RFC3339Nano)
				job.NextAt = due
				job.Clock.LastEvaluatedAt = due.Add(-time.Hour)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			stop := runApplicationFixture(t, f, gateway)
			runtimeEventually(t, func() bool {
				if err := service.Step(ctx); err != nil {
					t.Fatal(err)
				}
				var completed int
				return f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE state='completed' AND source->>'kind'='application_trigger' AND source->>'application'='calendar'`).Scan(&completed) == nil && completed == 1
			})
			stop()
			service.CloseScheduler()
			service.Repository = calendarpg.New(f.pool)
			if err := service.Step(ctx); err != nil {
				t.Fatal(err)
			}
			page, err := service.Occurrences(ctx, owner.Access, value.Schedules[0].ID, 0, 50)
			if err != nil || len(page.Occurrences) != 1 || page.Occurrences[0].State != "accepted" || page.Occurrences[0].MainThreadID != f.main.ID || calls.Load() != 1 {
				t.Fatal(page, calls.Load(), err)
			}
			var inputs int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs`).Scan(&inputs); err != nil || inputs != 1 {
				t.Fatal("import or restart duplicated Main input", inputs, err)
			}
		})
	}
}

func calendarImportValue(f memoryFixture) calendar.FleetImport {
	now := time.Now().UTC().Truncate(time.Second)
	return calendar.FleetImport{Source: "fixture/calendar", SourceSHA256: strings.Repeat("a", 64), CapturedAt: now, Schedules: []calendar.ImportedSchedule{{ID: uuid.NewString(), Definition: calendar.Definition{Name: "Imported schedule", Content: "Review original schedule", Mode: "main", AgentID: f.scope.AgentID, CatchUp: "none", Rule: recurrence.Rule{Frequency: recurrence.FrequencyOnce, At: now.Add(time.Hour).Format(time.RFC3339)}}, Scope: f.scope, Enabled: true, Clock: recurrence.State{LastEvaluatedAt: now.Add(-time.Hour)}, NextAt: now.Add(time.Hour)}}}
}

func TestManagedCalendarImportRetryAndNoReplay(t *testing.T) {
	for _, initialized := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "default-empty"}[initialized], func(t *testing.T) {
			f, service, store := calendarFixture(t)
			ctx := context.Background()
			owner, value := fleetOwner(f), calendarImportValue(f)
			if err := runtimepg.Migrate(ctx, f.pool); err != nil {
				t.Fatal(err)
			}
			if initialized {
				if _, err := service.Status(ctx, f.human); err != nil {
					t.Fatal(err)
				}
			}
			var before int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			for range 4 {
				wg.Go(func() {
					if err := store.ImportFleet(ctx, owner, value); err != nil {
						t.Error(err)
					}
				})
			}
			wg.Wait()
			service.Repository = calendarpg.New(f.pool)
			if err := service.Step(ctx); err != nil {
				t.Fatal(err)
			}
			if err := store.View(ctx, owner, func(s *calendar.State) error {
				j := s.Jobs[value.Schedules[0].ID]
				if len(s.Jobs) != 1 || len(s.Deliveries) != 0 || len(s.Commands) != 0 || j == nil || !j.NextAt.Equal(value.Schedules[0].NextAt) || j.CatchUp != "none" {
					t.Fatal(s)
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			var after, receipts int
			if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM runtime.inputs),(SELECT count(*) FROM calendar.imports)`).Scan(&after, &receipts); err != nil || before != after || receipts != 1 {
				t.Fatal(before, after, receipts, err)
			}
			id := value.Schedules[0].ID
			if _, err := service.Change(ctx, f.human, nil, "later-pause", calendar.Change{ID: id, Version: 1, Action: "pause"}); err != nil {
				t.Fatal(err)
			}
			if err := store.ImportFleet(ctx, owner, value); err != nil {
				t.Fatal(err)
			}
			page, err := service.Schedules(ctx, f.human, 0, 50)
			if err != nil || len(page.Schedules) != 1 || page.Schedules[0].Version != 2 || page.Schedules[0].Status != "paused" {
				t.Fatal(page, err)
			}
			for _, field := range []string{"source", "hash", "payload", "capture"} {
				b, _ := json.Marshal(value)
				var changed calendar.FleetImport
				if err := json.Unmarshal(b, &changed); err != nil {
					t.Fatal(err)
				}
				switch field {
				case "source":
					changed.Source += "/other"
				case "hash":
					changed.SourceSHA256 = strings.Repeat("b", 64)
				case "payload":
					changed.Schedules[0].Content = "Different content"
				case "capture":
					changed.CapturedAt = changed.CapturedAt.Add(time.Second)
				}
				if err := store.ImportFleet(ctx, owner, changed); !errors.Is(err, application.ErrConflict) {
					t.Fatal(field, err)
				}
			}
		})
	}
}

func TestManagedCalendarImportRejectsNonemptyStateAtomically(t *testing.T) {
	for _, kind := range []string{"disabled", "command", "delivery", "schedule", "insert-failure", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			f, _, store := calendarFixture(t)
			ctx := context.Background()
			owner, value := fleetOwner(f), calendarImportValue(f)
			if kind == "insert-failure" {
				if _, err := f.pool.Exec(ctx, `CREATE FUNCTION calendar.reject_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test import receipt failure'; END $$; CREATE TRIGGER reject_import BEFORE INSERT ON calendar.imports FOR EACH ROW EXECUTE FUNCTION calendar.reject_import()`); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := store.Update(ctx, owner, func(s *calendar.State) error {
					switch kind {
					case "disabled":
						s.Control.Enabled = false
					case "command":
						return s.CancelCommand(owner, "cancel-first")
					case "delivery":
						s.Deliveries[uuid.NewString()] = &calendar.Delivery{Finished: true, Settled: true}
					case "schedule":
						s.Jobs[uuid.NewString()] = &calendar.Job{}
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			var before []byte
			if err := f.pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(f)),'[]'::jsonb) FROM calendar.fleets f`).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if kind == "foreign" {
				owner.UserID = uuid.NewString()
				value.Schedules[0].Scope.UserID = owner.UserID
			}
			err := store.ImportFleet(ctx, owner, value)
			if err == nil {
				t.Fatal("existing state overwritten")
			}
			if kind != "insert-failure" && kind != "foreign" && !errors.Is(err, application.ErrConflict) {
				t.Fatal(err)
			}
			if kind == "foreign" && !errors.Is(err, application.ErrDenied) {
				t.Fatal(err)
			}
			var after []byte
			var count int
			if err := f.pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(f)),'[]'::jsonb),(SELECT count(*) FROM calendar.imports) FROM calendar.fleets f`).Scan(&after, &count); err != nil || string(before) != string(after) || count != 0 {
				t.Fatal("partial import", count, err)
			}
		})
	}
}

func TestManagedCalendarImportPurgeRejectsRetry(t *testing.T) {
	for _, whole := range []bool{false, true} {
		for _, concurrent := range []bool{false, true} {
			f, _, store := calendarFixture(t)
			ctx := context.Background()
			owner, value := fleetOwner(f), calendarImportValue(f)
			if !concurrent {
				if err := store.ImportFleet(ctx, owner, value); err != nil {
					t.Fatal(err)
				}
			}
			target := lifecycle.Target{ID: uuid.NewString(), TenantID: owner.TenantID, UserID: owner.UserID, FleetID: owner.FleetID, AgentIDs: []string{f.scope.AgentID}, WholeFleet: whole}
			var wg sync.WaitGroup
			if concurrent {
				wg.Go(func() {
					if err := store.ImportFleet(ctx, owner, value); err != nil && !errors.Is(err, application.ErrDenied) {
						t.Error(err)
					}
				})
			}
			if _, err := store.Purge(ctx, lifecycle.Request{Target: target, Phase: lifecycle.Erase}); err != nil {
				t.Fatal(err)
			}
			wg.Wait()
			if err := store.ImportFleet(ctx, owner, value); !errors.Is(err, application.ErrDenied) {
				t.Fatal("import revived purged Agent or Fleet", err)
			}
			if whole {
				var count int
				if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM calendar.imports`).Scan(&count); err != nil || count != 0 {
					t.Fatal("receipt survived Fleet purge", count, err)
				}
			}
		}
	}
}
