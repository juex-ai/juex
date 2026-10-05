//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/foundation/llm"
	"github.com/juex-ai/juex/internal/managedruntime"
	"github.com/juex-ai/juex/internal/management"
)

func TestManagedRuntimeWorkerDelegationAndResultSurviveIdle(t *testing.T) {
	var mainCalls, workerCalls atomic.Int32
	workerRelease := make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(workerRelease) }) }
	defer release()
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		for _, message := range request.Messages {
			content, _ := json.Marshal(message.Content)
			if message.Role == "user" && strings.Contains(string(content), "WORKER_PRIVATE_QUERY") {
				workerCalls.Add(1)
				select {
				case <-workerRelease:
				case <-r.Context().Done():
					return
				}
				streamManagedReply(w, "WORKER_ANSWER_431")
				return
			}
		}
		if mainCalls.Add(1) == 1 {
			streamManagedTool(w, "thread_create", map[string]any{"name": "Independent research", "query": "WORKER_PRIVATE_QUERY", "subscribe": true})
			return
		}
		encoded, _ := json.Marshal(request.Messages)
		if strings.Contains(string(encoded), "WORKER_ANSWER_431") {
			streamManagedReply(w, "Confirmed the Worker result")
		} else {
			streamManagedReply(w, "Worker has accepted the task")
		}
	})
	t.Cleanup(func() {
		if t.Failed() {
			rows, err := f.pool.Query(context.Background(), `SELECT kind,data FROM runtime.events ORDER BY created_at`)
			if err == nil {
				defer rows.Close()
				for rows.Next() {
					var kind string
					var data []byte
					_ = rows.Scan(&kind, &data)
					t.Log(kind, string(data))
				}
			}
			t.Log("model calls", mainCalls.Load(), workerCalls.Load())
		}
	})
	f.submit(t, "delegate", f.main.ID, "Delegate research to an independent Worker, then report its result")
	f.run(t)
	runtimeEventually(t, func() bool {
		var state string
		return f.pool.QueryRow(context.Background(), `SELECT state FROM runtime.threads WHERE id=$1`, f.main.ID).Scan(&state) == nil && state == "idle" && mainCalls.Load() == 2 && workerCalls.Load() == 1
	})
	release()
	runtimeEventually(t, func() bool {
		var completed int
		return f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='thread_result' AND state='completed'`).Scan(&completed) == nil && completed == 1
	})
	if workerCalls.Load() != 1 || mainCalls.Load() != 3 {
		t.Fatal("delegation replayed or consumed model polling", workerCalls.Load(), mainCalls.Load())
	}
	var workers, workerInputs int
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.threads WHERE kind='worker'`).Scan(&workers); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.inputs i JOIN runtime.threads t ON t.id=i.thread_id WHERE t.kind='worker'`).Scan(&workerInputs); err != nil {
		t.Fatal(err)
	}
	if workers != 1 || workerInputs != 1 {
		t.Fatal("worker creation was not atomic and unique", workers, workerInputs)
	}
}

func TestManagedRuntimeWorkerAcceptanceOrdersCancellation(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(cancelFirst), func(t *testing.T) {
			f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
			ctx := context.Background()
			scope, job := prepareRuntimeCall(t, f, f.main.ID, "delegate", llm.Block{Type: llm.BlockToolUse, ToolUseID: "delegate", ToolName: "thread_create"})
			action := managedruntime.ThreadAction{Kind: "thread_create", Name: "Research", Query: "independent task", Subscribe: true}
			if cancelFirst {
				if err := f.store.CancelThread(ctx, scope, f.main.ID); err != nil {
					t.Fatal(err)
				}
			}
			first, err := f.store.ApplyThreadAction(ctx, job, action, scope)
			if cancelFirst {
				if !errors.Is(err, managedruntime.ErrFence) {
					t.Fatal("cancelled source admitted Worker", string(first), err)
				}
				var count int
				if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.threads WHERE kind='worker'`).Scan(&count); err != nil || count != 0 {
					t.Fatal(count, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := f.store.ApplyThreadAction(ctx, job, action, scope)
			if err != nil || !equalJSON(first, repeated) {
				t.Fatal("lost response replayed the action", string(first), string(repeated), err)
			}
			if err := f.store.CancelThread(ctx, scope, f.main.ID); err != nil {
				t.Fatal(err)
			}
			var result struct {
				Receipt managedruntime.InputReceipt `json:"receipt"`
			}
			if err := json.Unmarshal(first, &result); err != nil {
				t.Fatal(err)
			}
			finishCollaborationInput(t, f, scope, result.Receipt.ID, "COMPLETED_AFTER_PARENT_STOP")
			var state string
			if err := f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, result.Receipt.ID).Scan(&state); err != nil || state != "completed" {
				t.Fatal(state, err)
			}
			if _, err := f.store.ClaimThreadDelivery(ctx, "after-stop"); !errors.Is(err, managedruntime.ErrNoWork) {
				t.Fatal("cancelled Main got a late result", err)
			}
		})
	}
}

func equalJSON(a, b []byte) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return false
	}
	aa, _ := json.Marshal(av)
	bb, _ := json.Marshal(bv)
	return string(aa) == string(bb)
}

func finishCollaborationInput(t *testing.T, f *managedRuntimeFixture, scope managedruntime.Scope, input, text string) {
	t.Helper()
	ctx := context.Background()
	lease, err := f.store.Claim(ctx, scope.AgentID, "finish-collaboration", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	config, err := f.authority.Snapshot(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	work, err := f.store.BeginTurn(ctx, lease, scope, input, config)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := f.store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{MaxOutputTokens: work.Config.Models[work.ModelIndex].MaxOutput, Messages: work.History})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishAttempt(ctx, lease, attempt.ID, llm.Response{Message: llm.TextMessage(llm.RoleAssistant, text)}, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Release(ctx, lease); err != nil {
		t.Fatal(err)
	}
}

func TestManagedRuntimeWorkerResultIsDurableAndCancelledBeforeDelivery(t *testing.T) {
	for _, cancelFirst := range []bool{true, false} {
		t.Run(fmt.Sprint(cancelFirst), func(t *testing.T) {
			f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
			ctx := context.Background()
			scope, job := prepareRuntimeCall(t, f, f.main.ID, "delegate", llm.Block{Type: llm.BlockToolUse, ToolUseID: "delegate", ToolName: "thread_create"})
			encoded, err := f.store.ApplyThreadAction(ctx, job, managedruntime.ThreadAction{Kind: "thread_create", Name: "Research", Query: "query", Subscribe: true}, scope)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Receipt managedruntime.InputReceipt `json:"receipt"`
			}
			if err := json.Unmarshal(encoded, &result); err != nil {
				t.Fatal(err)
			}
			finishCollaborationInput(t, f, scope, result.Receipt.ID, "DURABLE_RESULT")
			delivery, err := f.store.ClaimThreadDelivery(ctx, "first-process")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.pool.Exec(ctx, `UPDATE runtime.thread_deliveries SET lease_until='-infinity' WHERE id=$1`, delivery.ID); err != nil {
				t.Fatal(err)
			}
			recovered, err := f.store.ClaimThreadDelivery(ctx, "recovered-process")
			if err != nil || recovered.ID != delivery.ID || recovered.LeaseEpoch <= delivery.LeaseEpoch {
				t.Fatal(recovered, err)
			}
			if err := f.store.FinishThreadDelivery(ctx, delivery, true); !errors.Is(err, managedruntime.ErrFence) {
				t.Fatal("stale delivery was accepted", err)
			}
			if cancelFirst {
				if err := f.store.CancelThread(ctx, scope, f.main.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.store.FinishThreadDelivery(ctx, recovered, true); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='thread_result'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			expected := 1
			if cancelFirst {
				expected = 0
			}
			if count != expected {
				t.Fatal("result admission ignored cancellation", count, expected)
			}
			if err := f.store.FinishThreadDelivery(ctx, recovered, true); !errors.Is(err, managedruntime.ErrFence) {
				t.Fatal("result delivered twice", err)
			}
		})
	}
}

func TestManagedRuntimePeerMessageContinuesAfterSenderArchive(t *testing.T) {
	var targetCalls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		encoded, _ := json.Marshal(request.Messages)
		if !strings.Contains(string(encoded), "EXPLICIT_PEER_MESSAGE") || !strings.Contains(string(encoded), "sender_agent_id") {
			t.Error("peer message lost explicit provenance", string(encoded))
		}
		targetCalls.Add(1)
		streamManagedReply(w, "Target accepted")
	})
	ctx := context.Background()
	peer, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Peer", ModelID: f.agent.ModelID})
	if err != nil {
		t.Fatal(err)
	}
	scope, job := prepareRuntimeCall(t, f, f.main.ID, "send-peer", llm.Block{Type: llm.BlockToolUse, ToolUseID: "send", ToolName: "agent_send"})
	target, err := f.authority.Authorize(ctx, f.actor, f.tenant, peer.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	peers, err := f.authority.Peers(ctx, scope)
	if err != nil || len(peers) != 1 || peers[0].ID != peer.ID {
		t.Fatal(peers, err)
	}
	action := managedruntime.ThreadAction{Kind: "agent_send", Query: "EXPLICIT_PEER_MESSAGE"}
	encoded, err := f.store.ApplyThreadAction(ctx, job, action, target)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := f.store.ApplyThreadAction(ctx, job, action, target)
	if err != nil || !equalJSON(encoded, repeated) {
		t.Fatal("peer send is not idempotent", err)
	}
	if _, err := f.directory.SetAgentArchived(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, true); err != nil {
		t.Fatal(err)
	}
	f.run(t)
	var receipt managedruntime.InputReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool {
		var state string
		return f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, receipt.ID).Scan(&state) == nil && state == "completed"
	})
	if targetCalls.Load() != 1 {
		t.Fatal("sender lifecycle altered target work", targetCalls.Load())
	}
}

func TestManagedRuntimeCollaborationCannotCrossFleet(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	outsider, err := f.directory.CreateUser(ctx, "other-owner@example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.directory.Invite(ctx, f.actor, f.tenant, outsider.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.AcceptInvitation(ctx, outsider.ID, token); err != nil {
		t.Fatal(err)
	}
	agent, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, outsider.ID, management.AgentConfig{Name: "Private"})
	if err != nil {
		t.Fatal(err)
	}
	scope, job := prepareRuntimeCall(t, f, f.main.ID, "cross-fleet", llm.Block{Type: llm.BlockToolUse, ToolUseID: "send", ToolName: "agent_send"})
	target, err := f.authority.Authorize(ctx, f.actor, f.tenant, agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.ApplyThreadAction(ctx, job, managedruntime.ThreadAction{Kind: "agent_send", Query: "disallowed"}, target); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("cross Fleet message accepted", err)
	}
	peers, err := f.authority.Peers(ctx, scope)
	if err != nil || len(peers) != 0 {
		t.Fatal("admin collaboration listed another owner's Agents", peers, err)
	}
}

func TestManagedRuntimeWorkerDepthAndArchiveHTTP(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	worker := managementCall[managedruntime.Thread](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/workers", f.origin, map[string]string{"request_id": "first-worker", "name": "Research"}, 200)
	managementCall[any](t, f.client, "POST", f.base+"/threads/"+worker.ID+"/workers", f.origin, map[string]string{"request_id": "too-deep", "name": "Nested"}, 400)
	updated, err := f.directory.ConfigureAgent(ctx, f.actor, f.tenant, f.agent.ID, f.agent.Version, management.AgentConfig{Name: f.agent.Name, ModelID: f.agent.ModelID, WorkerDepth: 2})
	if err != nil || updated.WorkerDepth != 2 {
		t.Fatal(updated, err)
	}
	child := managementCall[managedruntime.Thread](t, f.client, "POST", f.base+"/threads/"+worker.ID+"/workers", f.origin, map[string]string{"request_id": "nested-worker", "name": "Nested"}, 200)
	managementCall[any](t, f.client, "POST", f.base+"/threads/"+child.ID+"/workers", f.origin, map[string]string{"request_id": "too-deep-again", "name": "Third"}, 400)
	managementCall[any](t, f.client, "POST", f.base+"/threads/"+f.main.ID+"/archive", f.origin, map[string]bool{"archived": true}, 400)
	managementCall[any](t, f.client, "POST", f.base+"/threads/"+worker.ID+"/archive", f.origin, map[string]bool{"archived": true}, 409)
	f.submit(t, "unfinished", child.ID, "Not accepted for archival while queued")
	managementCall[any](t, f.client, "POST", f.base+"/threads/"+child.ID+"/archive", f.origin, map[string]bool{"archived": true}, 409)
	managementCall[any](t, f.client, "POST", f.base+"/threads/"+child.ID+"/cancel", f.origin, map[string]any{}, 200)
	archived := managementCall[managedruntime.Thread](t, f.client, "POST", f.base+"/threads/"+child.ID+"/archive", f.origin, map[string]bool{"archived": true}, 200)
	if archived.Retention != "archived" {
		t.Fatal(archived)
	}
	managementCall[any](t, f.client, "POST", f.base+"/inputs", f.origin, managedruntime.InputRequest{RequestID: "archived-input", ThreadID: child.ID, Text: "Must not run"}, 403)
	if timeline := f.timeline(t, child.ID); len(timeline.Events) == 0 {
		t.Fatal("archive lost history")
	}
	managementCall[managedruntime.Thread](t, f.client, "POST", f.base+"/threads/"+worker.ID+"/archive", f.origin, map[string]bool{"archived": true}, 200)
	managementCall[any](t, f.client, "POST", f.base+"/threads/"+child.ID+"/archive", f.origin, map[string]bool{"archived": false}, 409)
	managementCall[managedruntime.Thread](t, f.client, "POST", f.base+"/threads/"+worker.ID+"/archive", f.origin, map[string]bool{"archived": false}, 200)
	restored := managementCall[managedruntime.Thread](t, f.client, "POST", f.base+"/threads/"+child.ID+"/archive", f.origin, map[string]bool{"archived": false}, 200)
	if restored.Retention != "active" || restored.PendingInputs != 0 {
		t.Fatal("restore replayed cancelled work", restored)
	}
}

func TestManagedRuntimeWorkerResubscriptionDoesNotReplayOldResults(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	scope, job := prepareRuntimeCall(t, f, f.main.ID, "delegate", llm.Block{Type: llm.BlockToolUse, ToolUseID: "delegate", ToolName: "thread_create"})
	encoded, err := f.store.ApplyThreadAction(ctx, job, managedruntime.ThreadAction{Kind: "thread_create", Name: "Research", Query: "First task", Subscribe: true}, scope)
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Thread  managedruntime.Thread       `json:"thread"`
		Receipt managedruntime.InputReceipt `json:"receipt"`
	}
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	finishCollaborationInput(t, f, scope, result.Receipt.ID, "OLD_RESULT")
	old, err := f.store.ClaimThreadDelivery(ctx, "old-result")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishTool(ctx, job, managedruntime.ToolOutcome{State: "ready", Content: string(encoded)}); err != nil {
		t.Fatal(err)
	}
	var input string
	if err := f.pool.QueryRow(ctx, `SELECT input_id FROM runtime.turns WHERE id=$1`, job.TurnID).Scan(&input); err != nil {
		t.Fatal(err)
	}
	finishCollaborationInput(t, f, scope, input, "Delegated")
	_, resub := prepareRuntimeCall(t, f, f.main.ID, "resubscribe", llm.Block{Type: llm.BlockToolUse, ToolUseID: "subscribe", ToolName: "thread_subscribe"})
	if _, err := f.store.ApplyThreadAction(ctx, resub, managedruntime.ThreadAction{Kind: "thread_subscribe", ThreadID: result.Thread.ID}, scope); err != nil {
		t.Fatal(err)
	}
	if err := f.store.FinishThreadDelivery(ctx, old, true); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='thread_result'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("old generation replayed", count, err)
	}
	receipt, err := f.store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "second-worker-turn", ThreadID: result.Thread.ID, Text: "Second task"})
	if err != nil {
		t.Fatal(err)
	}
	finishCollaborationInput(t, f, scope, receipt.ID, "NEW_RESULT")
	newDelivery, err := f.store.ClaimThreadDelivery(ctx, "new-result")
	if err != nil || newDelivery.Generation <= old.Generation || !strings.Contains(newDelivery.Text, "NEW_RESULT") {
		t.Fatal(newDelivery, err)
	}
	if err := f.store.FinishThreadDelivery(ctx, newDelivery, true); err != nil {
		t.Fatal(err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='thread_result'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}

func TestManagedRuntimeCollaborationOppositeDirectionsDoNotDeadlock(t *testing.T) {
	f := managedRuntimeHTTP(t, func(http.ResponseWriter, *http.Request) {})
	ctx := context.Background()
	scope, err := f.authority.Authorize(ctx, f.actor, f.tenant, f.agent.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := f.store.CreateWorker(ctx, scope, f.main.ID, "worker", "Worker")
	if err != nil {
		t.Fatal(err)
	}
	_, mainWork := prepareRuntimeCall(t, f, f.main.ID, "main-send", llm.Block{Type: llm.BlockToolUse, ToolUseID: "main-send", ToolName: "thread_send"})
	_, workerWork := prepareRuntimeCall(t, f, worker.ID, "worker-send", llm.Block{Type: llm.BlockToolUse, ToolUseID: "worker-send", ToolName: "thread_send"})
	call, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := make(chan struct{})
	done := make(chan error, 2)
	for _, pair := range []struct {
		work   managedruntime.ToolWork
		target string
	}{{mainWork, worker.ID}, {workerWork, f.main.ID}} {
		go func() {
			<-start
			_, err := f.store.ApplyThreadAction(call, pair.work, managedruntime.ThreadAction{Kind: "thread_send", ThreadID: pair.target, Query: "Explicit concurrent message"}, scope)
			done <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal("mutual message admission failed", err)
		}
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.inputs WHERE source->>'kind'='worker_message'`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
}

func TestManagedRuntimePeerDiscoveryPurposeAndSendTools(t *testing.T) {
	var mainCalls, targetCalls atomic.Int32
	var peerID string
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name       string         `json:"name"`
					Parameters map[string]any `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		for _, message := range request.Messages {
			content, _ := json.Marshal(message.Content)
			if message.Role == "user" && strings.Contains(string(content), "EXPLICIT_TOOL_PAYLOAD") {
				targetCalls.Add(1)
				streamManagedReply(w, "Received")
				return
			}
		}
		switch mainCalls.Add(1) {
		case 1:
			for _, tool := range request.Tools {
				if tool.Function.Name == "agent_list" {
					parameters := tool.Function.Parameters
					if required, ok := parameters["required"].([]any); !ok || len(required) != 1 || required[0] != "reason" {
						t.Error("lookup purpose must be required in tool schema", parameters)
					}
					properties, _ := parameters["properties"].(map[string]any)
					if properties["reason"] == nil {
						t.Error("read-only lookup purpose missing from declaration")
					}
				}
			}
			streamManagedTool(w, "agent_list", map[string]any{"reason": "Find the intended collaborator"})
		case 2:
			encoded, _ := json.Marshal(request.Messages)
			if !strings.Contains(string(encoded), peerID) {
				t.Error("peer discovery did not return the scoped identity", string(encoded))
			}
			streamManagedTool(w, "agent_send", map[string]any{"agent_id": peerID, "query": "EXPLICIT_TOOL_PAYLOAD"})
		default:
			streamManagedReply(w, "Message durably accepted")
		}
	})
	peer, err := f.directory.CreateAgent(context.Background(), f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Peer", ModelID: f.agent.ModelID})
	if err != nil {
		t.Fatal(err)
	}
	peerID = peer.ID
	f.submit(t, "send-through-tools", f.main.ID, "Discover the collaborator and send an explicit message")
	f.run(t)
	runtimeEventually(t, func() bool {
		var count int
		return f.pool.QueryRow(context.Background(), `SELECT count(*) FROM runtime.inputs WHERE state='completed'`).Scan(&count) == nil && count == 2
	})
	if mainCalls.Load() != 3 || targetCalls.Load() != 1 {
		t.Fatal("tool discovery or send replayed", mainCalls.Load(), targetCalls.Load())
	}
}
