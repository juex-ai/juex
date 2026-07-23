package statusapi

import (
	"context"
	"testing"
)

func TestActivityStoreStartsIdle(t *testing.T) {
	store := NewActivityStore()
	stream := store.OpenStream(ActivityStreamOptions{})
	defer stream.Close()

	activity, ok := stream.Next(context.Background())
	if !ok || activity.State != ActivityIdle {
		t.Fatalf("initial activity = %+v, %t", activity, ok)
	}
	if _, ok := stream.Next(context.Background()); ok {
		t.Fatal("current-only stream returned more than one initial value")
	}
}

func TestActivityStoreIgnoresSelectedSessionCursorCollisions(t *testing.T) {
	store := NewActivityStore()
	store.Publish(AgentActivity{
		State: ActivityWorking,
		SelectedStatus: &Snapshot{
			Cursor:  "same",
			Session: SessionStatus{ID: "session-one"},
		},
	})
	store.Publish(AgentActivity{
		State: ActivityWorking,
		SelectedStatus: &Snapshot{
			Cursor:  "different",
			Session: SessionStatus{ID: "session-two"},
		},
	})
	store.Publish(AgentActivity{
		State: ActivityIdle,
		SelectedStatus: &Snapshot{
			Cursor:  "same",
			Session: SessionStatus{ID: "session-three"},
		},
	})

	stream := store.OpenStream(ActivityStreamOptions{After: "same"})
	defer stream.Close()
	activity, ok := stream.Next(context.Background())
	if !ok || activity.SelectedStatus == nil ||
		activity.SelectedStatus.Session.ID != "session-three" {
		t.Fatalf("current activity = %+v, %t", activity, ok)
	}
	if _, ok := stream.Next(context.Background()); ok {
		t.Fatal("activity stream attempted cursor history replay")
	}
}

func TestActivityStoreClonesNestedStatusForSubscribers(t *testing.T) {
	store := NewActivityStore()
	first := store.OpenStream(ActivityStreamOptions{Follow: true})
	defer first.Close()
	second := store.OpenStream(ActivityStreamOptions{Follow: true})
	defer second.Close()
	if _, ok := first.Next(context.Background()); !ok {
		t.Fatal("first stream omitted initial activity")
	}
	if _, ok := second.Next(context.Background()); !ok {
		t.Fatal("second stream omitted initial activity")
	}

	store.Publish(AgentActivity{
		State: ActivityWorking,
		SelectedStatus: &Snapshot{
			Session: SessionStatus{ID: "session-one"},
			Tools: []ToolCallStatus{{
				ToolUseID: "tool-one",
				Error:     &StatusError{Message: "original"},
			}},
			ContextUsage: &ContextUsage{
				Breakdown: []ContextUsagePart{{Key: "prompt", Tokens: 1}},
			},
		},
	})
	firstActivity, ok := first.Next(context.Background())
	if !ok {
		t.Fatal("first stream omitted update")
	}
	firstActivity.SelectedStatus.Tools[0].Error.Message = "mutated"
	firstActivity.SelectedStatus.ContextUsage.Breakdown[0].Tokens = 99

	secondActivity, ok := second.Next(context.Background())
	if !ok {
		t.Fatal("second stream omitted update")
	}
	if secondActivity.SelectedStatus.Tools[0].Error.Message != "original" ||
		secondActivity.SelectedStatus.ContextUsage.Breakdown[0].Tokens != 1 {
		t.Fatalf("second activity shared nested state: %+v", secondActivity)
	}
}
