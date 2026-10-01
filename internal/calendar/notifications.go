package calendar

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/juex-ai/juex/internal/foundation/application"
)

func (s *State) StageNotifications() {
	for _, d := range s.Deliveries {
		if !d.Finished {
			continue
		}
		switch d.State {
		case "completed", "cancelled", "missed", "needs_attention", "outcome_unknown":
		default:
			continue
		}
		if d.Notices == nil {
			d.Notices = map[string]*Notification{}
		}
		key := "result"
		if d.State == "outcome_unknown" {
			key = "unknown"
		}
		if d.Notices[key] != nil {
			continue
		}
		kind, title, summary := "completed", "日程已执行", d.Name
		switch d.State {
		case "missed":
			kind, title = "attention", "日程已错过补跑期限"
		case "needs_attention":
			kind, title = "attention", "日程需要处理"
		case "outcome_unknown":
			kind, title = "attention", "日程执行结果未知，请核对原操作"
		case "cancelled":
			title = "日程执行已取消"
		case "completed":
			if d.Mode == "reminder" {
				kind, title, summary = "reminder", d.Name, d.Content
			}
		}
		identity := "calendar/notice/" + d.ID
		if key == "unknown" {
			identity += "/unknown"
		}
		event := application.Event{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte(identity)).String(), Application: "calendar", ResourceID: d.ID, Kind: kind, Title: title, Summary: summary, Scope: d.Scope, Epoch: d.Epoch, CreatedAt: d.UpdatedAt}
		d.Notices[key] = &Notification{Event: event, MainDone: d.Scope.AgentID == ""}
	}
}

func (s *Service) notify(ctx context.Context) error {
	repo, ok := s.Repository.(PendingRepository)
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
			d := state.Deliveries[notice.Event.ResourceID]
			if d == nil || deliveryNotice(d, notice.Event.ID) == nil {
				return application.ErrDenied
			}
			deliveryNotice(d, notice.Event.ID).AttemptedAt = time.Now()
			notice = *deliveryNotice(d, notice.Event.ID)
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
				d := state.Deliveries[notice.Event.ResourceID]
				if d == nil || deliveryNotice(d, notice.Event.ID) == nil {
					return application.ErrDenied
				}
				if target == "main" {
					deliveryNotice(d, notice.Event.ID).MainDone = true
				} else {
					deliveryNotice(d, notice.Event.ID).InboxDone = true
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

func deliveryNotice(d *Delivery, id string) *Notification {
	for _, notice := range d.Notices {
		if notice.Event.ID == id {
			return notice
		}
	}
	return nil
}
