//go:build postgres

package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/calendar"
)

func TestManagedCalendarUnfinishedOccurrenceDoesNotBlockPendingNotices(t *testing.T) {
	for _, withCompleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unfinished_only", true: "unfinished_and_completed"}[withCompleted], func(t *testing.T) {
			f, service, store := calendarFixture(t)
			ctx := context.Background()
			unfinished, completed := calendarChange(f.scope.AgentID), calendarChange(f.scope.AgentID)
			changes := []calendar.Change{unfinished}
			if withCompleted {
				changes = append(changes, completed)
			}
			for _, change := range changes {
				if _, err := service.Change(ctx, f.human, nil, change.ID, change); err != nil {
					t.Fatal(err)
				}
			}
			var completedID string
			if err := store.Update(ctx, f.scope, func(state *calendar.State) error {
				for _, change := range changes {
					if err := state.Advance(change.ID, time.Now().Add(time.Minute)); err != nil {
						return err
					}
				}
				for _, delivery := range state.Deliveries {
					if delivery.ScheduleID == completed.ID {
						completedID = delivery.ID
						delivery.State, delivery.Finished = "completed", true
					}
				}
				state.StageNotifications()
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			notices, err := store.PendingNotifications(ctx, 100)
			if err != nil {
				t.Fatal("an unfinished occurrence prevented notification scanning", err)
			}
			if withCompleted {
				if completedID == "" || len(notices) != 1 || notices[0].Event.ResourceID != completedID {
					t.Fatal("completed occurrence notification was lost", notices)
				}
			} else if len(notices) != 0 {
				t.Fatal("unfinished occurrence emitted a completion notice", notices)
			}
		})
	}
}
