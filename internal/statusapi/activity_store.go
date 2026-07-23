package statusapi

import (
	"context"
	"reflect"

	"github.com/juex-ai/juex/internal/statusstream"
)

type ActivityStreamOptions struct {
	After  string
	Follow bool
}

type ActivityStore struct {
	stream *statusstream.Store[AgentActivity]
}

type ActivityStream struct {
	stream *statusstream.Stream[AgentActivity]
}

func NewActivityStore() *ActivityStore {
	initial := AgentActivity{State: ActivityIdle}
	return &ActivityStore{
		stream: statusstream.New(initial, statusstream.Options[AgentActivity]{
			Clone: cloneAgentActivity,
			Cursor: func(activity AgentActivity) string {
				if activity.SelectedStatus == nil {
					return ""
				}
				return activity.SelectedStatus.Cursor
			},
			Equal: func(left, right AgentActivity) bool {
				return reflect.DeepEqual(left, right)
			},
			HistoryLimit: 0,
		}),
	}
}

func (s *ActivityStore) Publish(activity AgentActivity) {
	if s != nil {
		s.stream.Publish(activity, false)
	}
}

func (s *ActivityStore) OpenStream(options ActivityStreamOptions) *ActivityStream {
	if s == nil {
		return &ActivityStream{}
	}
	return &ActivityStream{
		stream: s.stream.Open(statusstream.OpenOptions{
			After:  options.After,
			Follow: options.Follow,
		}),
	}
}

func (s *ActivityStream) Next(ctx context.Context) (AgentActivity, bool) {
	if s == nil || s.stream == nil {
		return AgentActivity{}, false
	}
	return s.stream.Next(ctx)
}

func (s *ActivityStream) Close() {
	if s != nil && s.stream != nil {
		s.stream.Close()
	}
}

func cloneAgentActivity(activity AgentActivity) AgentActivity {
	if activity.SelectedStatus == nil {
		return activity
	}
	snapshot := *activity.SelectedStatus
	if snapshot.Turn != nil {
		turn := *snapshot.Turn
		turn.Error = cloneStatusError(turn.Error)
		snapshot.Turn = &turn
	}
	snapshot.Tools = append([]ToolCallStatus(nil), snapshot.Tools...)
	for index := range snapshot.Tools {
		snapshot.Tools[index].Error = cloneStatusError(snapshot.Tools[index].Error)
	}
	if snapshot.ContextUsage != nil {
		contextUsage := *snapshot.ContextUsage
		contextUsage.Breakdown = append([]ContextUsagePart(nil), contextUsage.Breakdown...)
		snapshot.ContextUsage = &contextUsage
	}
	snapshot.LastError = cloneStatusError(snapshot.LastError)
	activity.SelectedStatus = &snapshot
	return activity
}

func cloneStatusError(statusErr *StatusError) *StatusError {
	if statusErr == nil {
		return nil
	}
	cloned := *statusErr
	return &cloned
}
