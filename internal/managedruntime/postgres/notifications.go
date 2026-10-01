package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/managedruntime"
)

func notificationID(key string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("juex/runtime/notification/"+key)).String()
}

// Only bounded facts enter the Inbox. Provider errors, model output and tool
// arguments remain in the authorized Thread, never in the notification payload.
func stageEventNotification(ctx context.Context, tx pgx.Tx, thread, kind string, data []byte) error {
	var v struct {
		InputID string `json:"input_id"`
		TurnID  string `json:"turn_id"`
		ID      string `json:"id"`
	}
	var title, description, identity string
	eventKind := "attention"
	switch kind {
	case "turn.completed":
		title, description, eventKind = "本轮处理已结束", "查看对话中的执行结果；后台操作状态以原操作记录为准。", "completed"
	case "turn.failed":
		title, description = "任务执行失败", "请打开对话查看失败原因和原操作状态。"
	case "input.held":
		title, description = "任务需要处理", "任务已暂停等待处理，不会自动重放此前操作。"
	case "tool.unknown":
		title, description = "外部操作结果未知", "请核对原操作；系统不会自动重试可能已产生效果的操作。"
	default:
		return nil
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	identity = v.InputID
	if kind == "tool.unknown" {
		identity = v.ID
	}
	return stageNotification(ctx, tx, thread, v.InputID, v.TurnID, kind+"/"+identity, eventKind, title, description, false, 0)
}

func stageNotification(ctx context.Context, tx pgx.Tx, thread, input, turn, key, kind, title, description string, allowApplications bool, delay time.Duration) error {
	var scope application.Scope
	var name string
	var generation int64
	err := tx.QueryRow(ctx, `SELECT a.tenant_id,a.user_id,a.fleet_id,a.id,i.actor_id,i.actor_authorization_epoch,i.membership_execution_epoch,i.membership_version,i.agent_execution_epoch,t.name,t.generation
 FROM runtime.threads t JOIN runtime.agents a ON a.id=t.agent_id JOIN runtime.inputs i ON i.thread_id=t.id
 WHERE t.id=$1 AND (i.id::text=$2 OR i.id=(SELECT input_id FROM runtime.turns WHERE id::text=$3))
 AND ($4 OR NOT EXISTS(SELECT 1 FROM runtime.application_jobs j WHERE j.thread_id=t.id))`, thread, input, turn, allowApplications).Scan(&scope.TenantID, &scope.UserID, &scope.FleetID, &scope.AgentID, &scope.ActorID, &scope.ActorEpoch, &scope.MemberEpoch, &scope.MemberVersion, &scope.AgentEpoch, &name, &generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	event := application.Event{ID: notificationID(key), Application: "runtime", ResourceID: thread, Kind: kind, Title: title, Summary: name + "：" + description, Scope: scope, Epoch: generation, CreatedAt: time.Now().UTC()}
	if !event.Valid() {
		return managedruntime.ErrInvalid
	}
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	tool := ""
	if delay > 0 {
		tool = strings.TrimPrefix(key, "tool.waiting/")
	}
	// A brief first phase may recover before delivery, then another device may
	// be awaited. Rearm only never-claimed candidates: a lost Inbox reply must
	// retain its original immutable payload and at-most-once visible identity.
	_, err = tx.Exec(ctx, `INSERT INTO runtime.notification_outbox(event_id,agent_id,event,not_before,thread_id,tool_id) VALUES($1,$2,$3,clock_timestamp()+make_interval(secs=>$4),$5,NULLIF($6,'')::uuid)
 ON CONFLICT(event_id) DO UPDATE SET state='pending',event=EXCLUDED.event,not_before=EXCLUDED.not_before
 WHERE notification_outbox.tool_id IS NOT NULL AND notification_outbox.state='skipped' AND notification_outbox.lease_epoch=0`, event.ID, scope.AgentID, data, delay.Seconds(), thread, tool)
	return err
}

func toolWaitNotification(ctx context.Context, tx pgx.Tx, work managedruntime.ToolWork, outcome managedruntime.ToolOutcome) error {
	key := "tool.waiting/" + work.ID
	if !work.Cancelled && outcome.WaitReason == "environment" {
		return stageNotification(ctx, tx, work.ThreadID, "", work.TurnID, key, "attention", "任务正在等待执行环境", "执行请求仍在等待。请查看设备连接和原操作，恢复后会继续同一请求。", true, 30*time.Second)
	}
	if work.Cancelled || outcome.State != "waiting" || outcome.WaitReason == "execution" {
		_, err := tx.Exec(ctx, `UPDATE runtime.notification_outbox SET state='skipped' WHERE event_id=$1 AND state='pending'`, notificationID(key))
		return err
	}
	return nil
}

func (s *Store) ClaimNotification(ctx context.Context) (managedruntime.NotificationDelivery, error) {
	var d managedruntime.NotificationDelivery
	var data []byte
	var id, thread, agent, tool string
	tx, err := s.begin(ctx)
	if err != nil {
		return d, err
	}
	defer rollback(tx)
	err = tx.QueryRow(ctx, `SELECT event_id,thread_id,agent_id,COALESCE(tool_id::text,'') FROM runtime.notification_outbox WHERE state='pending' AND not_before<=clock_timestamp() AND lease_until<=clock_timestamp() ORDER BY not_before,event_id LIMIT 1`).Scan(&id, &thread, &agent, &tool)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, managedruntime.ErrNoWork
	}
	if err != nil {
		return d, err
	}
	// Tool transitions already take this Thread lock before touching their
	// notification. Never hold the outbox row while waiting for the Thread.
	if _, err := readThread(ctx, tx, agent, thread); err != nil {
		return d, err
	}
	if tool != "" {
		var waiting bool
		err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM runtime.tools k JOIN runtime.turns t ON t.id=k.turn_id JOIN runtime.inputs i ON i.id=t.input_id
 WHERE k.id=$1 AND k.state='waiting' AND k.waiting_reason='environment' AND NOT k.cancel_requested AND t.state='waiting' AND i.state='active'
 AND NOT EXISTS(SELECT 1 FROM runtime.application_jobs a WHERE a.thread_id=t.thread_id AND a.cancelled))`, tool).Scan(&waiting)
		if err != nil {
			return d, err
		}
		if !waiting {
			if _, err := tx.Exec(ctx, `UPDATE runtime.notification_outbox SET state='skipped' WHERE event_id=$1 AND state='pending'`, id); err != nil {
				return d, err
			}
			if err := tx.Commit(ctx); err != nil {
				return d, err
			}
			return d, managedruntime.ErrNoWork
		}
	}
	err = tx.QueryRow(ctx, `UPDATE runtime.notification_outbox SET lease_epoch=lease_epoch+1,lease_until=clock_timestamp()+interval '15 seconds' WHERE event_id=$1 AND state='pending' AND not_before<=clock_timestamp() AND lease_until<=clock_timestamp() RETURNING event,lease_epoch`, id).Scan(&data, &d.LeaseEpoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, managedruntime.ErrNoWork
	}
	if err != nil {
		return d, err
	}
	err = json.Unmarshal(data, &d.Event)
	if err != nil {
		return d, err
	}
	return d, tx.Commit(ctx)
}

func (s *Store) FinishNotification(ctx context.Context, d managedruntime.NotificationDelivery, accepted bool) error {
	result, err := s.pool.Exec(ctx, `UPDATE runtime.notification_outbox SET state=CASE WHEN state='skipped' THEN state WHEN $3 THEN 'sent' ELSE 'pending' END,lease_until='-infinity',not_before=clock_timestamp()+interval '5 seconds' WHERE event_id=$1 AND lease_epoch=$2 AND lease_until>clock_timestamp()`, d.Event.ID, d.LeaseEpoch, accepted)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return managedruntime.ErrFence
	}
	return nil
}
