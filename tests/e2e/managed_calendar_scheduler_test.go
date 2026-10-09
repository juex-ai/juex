//go:build postgres

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/calendar"
	calendarpg "github.com/juex-ai/juex/internal/calendar/postgres"
)

func TestManagedCalendarStepRecoversOnceThenDeliversPostRecoveryInstant(t *testing.T) {
	f, service, store := calendarFixture(t)
	ctx := context.Background()
	q := calendarChange("")
	q.Definition.Mode, q.Definition.CatchUp = "reminder", "none"
	if _, err := service.Change(ctx, f.human, nil, "create", q); err != nil {
		t.Fatal(err)
	}
	setDue := func(due time.Time) {
		t.Helper()
		if err := store.Update(ctx, f.scope, func(s *calendar.State) error {
			job := s.Jobs[q.ID]
			job.Clock.Anchor = due.Add(-time.Minute)
			job.Clock.LastEvaluatedAt = job.Clock.Anchor
			job.NextAt = due
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var databaseNow time.Time
	if err := f.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		t.Fatal(err)
	}
	// Keep the next minute well after recovery so this assertion only observes
	// missed work, not a legitimate due instant crossed during the first Step.
	setDue(databaseNow.Add(-48*time.Hour - 30*time.Second))
	if err := service.Step(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := service.Occurrences(ctx, f.human, q.ID, 0, 50)
	if err != nil || len(page.Occurrences) != 1 || page.Occurrences[0].State != "skipped" {
		t.Fatal("startup emitted unprepared missed work", page, err)
	}
	skippedID := page.Occurrences[0].ID
	// A due instant created after the first Step is later than its database-time
	// recovery cutoff. The next Step must reuse that cutoff, not skip live work.
	var due time.Time
	if err := f.pool.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&due); err != nil {
		t.Fatal(err)
	}
	setDue(due)
	runtimeEventually(t, func() bool {
		if err := service.Step(ctx); err != nil {
			t.Fatal(err)
		}
		page, err = service.Occurrences(ctx, f.human, q.ID, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		return len(page.Occurrences) == 2
	})
	for _, occurrence := range page.Occurrences {
		if occurrence.ID != skippedID && (occurrence.State != "completed" || !occurrence.ScheduledAt.Equal(due)) {
			t.Fatal("continuous scheduler treated live due work as recovery", occurrence)
		}
	}
}

func TestManagedCalendarSchedulerSessionFencesRecoveryAndStaleWrites(t *testing.T) {
	f, service, store := calendarFixture(t)
	ctx := context.Background()
	q := calendarChange(f.scope.AgentID)
	q.Definition.CatchUp = "none"
	if _, err := service.Change(ctx, f.human, nil, "create", q); err != nil {
		t.Fatal(err)
	}
	leader, err := store.OpenScheduler(ctx)
	if err != nil || leader == nil {
		t.Fatal(leader, err)
	}
	defer leader.Close()
	cutoff := leader.RecoveredAt()
	if standby, err := calendarpg.New(f.pool).OpenScheduler(ctx); err != nil || standby != nil {
		if standby != nil {
			standby.Close()
		}
		t.Fatal("second scheduler acquired session", err)
	}
	// A live leader's late timer must not acquire a new recovery boundary.
	due := cutoff.Add(-48 * time.Hour)
	if err := store.Update(ctx, f.scope, func(s *calendar.State) error {
		j := s.Jobs[q.ID]
		j.Clock.Anchor = due.Add(-time.Hour)
		j.Clock.LastEvaluatedAt = j.Clock.Anchor
		j.NextAt = due
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := leader.Update(ctx, f.scope, func(s *calendar.State) error { return s.Advance(q.ID, cutoff) }); err != nil {
		t.Fatal(err)
	}
	page, err := service.Occurrences(ctx, f.human, q.ID, 0, 50)
	if err != nil || len(page.Occurrences) != 1 || page.Occurrences[0].State != "pending" || !page.Occurrences[0].ScheduledAt.Equal(due) {
		t.Fatal(page, err)
	}
	// Terminate only the observed scheduler session in this fixture's disposable
	// database. This proves a connection failure cannot retain writable leadership.
	rows, err := f.pool.Query(ctx, `SELECT pid FROM pg_locks WHERE locktype='advisory' AND granted AND database=(SELECT oid FROM pg_database WHERE datname=current_database()) AND pid<>pg_backend_pid()`)
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for rows.Next() {
		var pid int
		if err := rows.Scan(&pid); err != nil {
			t.Fatal(err)
		}
		pids = append(pids, pid)
	}
	rows.Close()
	if len(pids) != 1 {
		t.Fatal("unexpected lock holders in isolated database", pids)
	}
	var killed bool
	if err := f.pool.QueryRow(ctx, `SELECT pg_terminate_backend($1)`, pids[0]).Scan(&killed); err != nil || !killed {
		t.Fatal(killed, err)
	}
	if err := leader.Update(ctx, f.scope, func(s *calendar.State) error { s.Jobs[q.ID].Name = "stale write"; return nil }); err == nil {
		t.Fatal("disconnected leader committed")
	}
	successor, err := store.OpenScheduler(ctx)
	if err != nil || successor == nil {
		t.Fatal(successor, err)
	}
	defer successor.Close()
	if !successor.RecoveredAt().After(cutoff) {
		t.Fatal("takeover kept old boundary")
	}
	if err := successor.Update(ctx, f.scope, func(s *calendar.State) error {
		if s.Jobs[q.ID].Name == "stale write" {
			t.Fatal("stale write leaked")
		}
		return s.Recover(q.ID, successor.RecoveredAt())
	}); err != nil {
		t.Fatal(err)
	}
	page, err = service.Occurrences(ctx, f.human, q.ID, 0, 50)
	if err != nil || len(page.Occurrences) != 1 || page.Occurrences[0].State != "pending" {
		t.Fatal("prepared delivery lost", page, err)
	}
}
