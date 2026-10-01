package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/application"
)

type Notification struct {
	Event       application.Event `json:"event"`
	MainDone    bool              `json:"main_done"`
	InboxDone   bool              `json:"inbox_done"`
	AttemptedAt time.Time         `json:"attempted_at"`
}
type Notifier interface {
	Main(context.Context, application.Event) error
	Inbox(context.Context, application.Event) error
}
type NotificationRepository interface {
	PendingNotifications(context.Context, int) ([]Notification, error)
}

// StageNotifications runs before the same repository commit as the business
// transition. The frozen notice never derives success from Worker shutdown.
func (s *State) StageNotifications() {
	for _, review := range s.Reviews {
		if !terminal(review.Receipt.State) || review.Notification != nil {
			continue
		}
		kind, title := "completed", "Memory 审核已完成"
		summary := fmt.Sprintf("共享知识更新了 %d 条。", len(review.Receipt.EntryIDs))
		switch review.Receipt.State {
		case "no_change":
			title = "Memory 审核完成，无需修改"
			summary = "本次没有变更共享知识。"
		case "failed":
			kind, title = "attention", "Memory 审核未完成"
			summary = "请在 Memory 审核记录中查看处理结果。"
		case "rejected":
			title = "Memory 审核未应用"
			summary = "本次审核没有写入共享知识。"
		}
		event := application.Event{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte("memory/"+review.Scope.FleetID+"/"+review.ID)).String(), Application: "memory", ResourceID: review.ID, Scope: review.Scope, Epoch: review.Epoch, Fence: review.Fence, Kind: kind, Title: title, Summary: summary, CreatedAt: review.Receipt.UpdatedAt}
		review.Notification = &Notification{Event: event}
	}
}

func (s *Service) notify(ctx context.Context) error {
	repo, ok := s.Repository.(NotificationRepository)
	if !ok || s.Notifier == nil {
		return nil
	}
	notices, err := repo.PendingNotifications(ctx, 20)
	if err != nil {
		return err
	}
	var failures []error
	for _, notice := range notices {
		if ctx.Err() != nil {
			break
		}
		err := s.Repository.Update(ctx, notice.Event.Scope, func(state *State) error {
			review := state.Reviews[notice.Event.ResourceID]
			if review == nil || review.Notification == nil {
				return application.ErrDenied
			}
			n := review.Notification
			n.AttemptedAt = time.Now()
			notice = *n
			return nil
		})
		if err != nil {
			failures = append(failures, err)
			continue
		}
		for _, target := range []string{"main", "inbox"} {
			if target == "main" && notice.MainDone || target == "inbox" && notice.InboxDone {
				continue
			}
			call, cancel := context.WithTimeout(ctx, time.Second)
			if target == "main" {
				err = s.Notifier.Main(call, notice.Event)
			} else {
				err = s.Notifier.Inbox(call, notice.Event)
			}
			cancel()
			if err != nil && !errors.Is(err, application.ErrDenied) {
				failures = append(failures, err)
				continue
			}
			err = s.Repository.Update(ctx, notice.Event.Scope, func(state *State) error {
				review := state.Reviews[notice.Event.ResourceID]
				if review == nil || review.Notification == nil {
					return application.ErrDenied
				}
				n := review.Notification
				if target == "main" {
					n.MainDone = true
				} else {
					n.InboxDone = true
				}
				return nil
			})
			if err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}
