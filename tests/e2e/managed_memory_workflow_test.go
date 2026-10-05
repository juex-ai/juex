//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/app/managed"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/application"
	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	"github.com/juex-ai/juex/internal/managedruntime"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/management"
	"github.com/juex-ai/juex/internal/memory"
	memorypg "github.com/juex-ai/juex/internal/memory/postgres"
	memoryrpc "github.com/juex-ai/juex/internal/memory/rpc"
)

func TestManagedMemoryRuntimeWorkerWorkflow(t *testing.T) {
	ctx := context.Background()
	var repository *memorypg.Store
	var mainCalls, workerCalls atomic.Int32
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		review := false
		for _, tool := range request.Tools {
			if tool.Function.Name == "memory_decide" {
				review = true
			}
		}
		if !review {
			if mainCalls.Add(1) == 1 {
				streamManagedTool(w, "memory_propose", map[string]any{"key": "reply-preference", "text": "The user prefers concise replies.", "reason": "The user explicitly requested remembering this."})
			} else {
				streamManagedReply(w, "Submitted for Memory review")
			}
			return
		}
		for _, tool := range request.Tools {
			if tool.Function.Name != "read_context" && tool.Function.Name != "memory_search" && tool.Function.Name != "memory_read" && tool.Function.Name != "memory_facts" && tool.Function.Name != "memory_domains" && tool.Function.Name != "memory_decide" {
				t.Error("review tool escape", tool.Function.Name)
			}
		}
		if workerCalls.Add(1) > 1 {
			streamManagedReply(w, "Memory review applied")
			return
		}
		jobs, err := repository.PendingReviews(ctx, 100)
		if err != nil || len(jobs) != 1 {
			t.Error(jobs, err)
			return
		}
		proposal := jobs[0].Proposal
		if len(proposal.Evidence) != 1 || proposal.Evidence[0].Kind != "user" || proposal.Evidence[0].Text != "Please remember that I prefer concise replies." {
			t.Error("original evidence", proposal)
		}
		decision := memoryDecision("concise-replies", proposal)
		entry := decision.Changes[0].Entry
		entry.Entities = []mc.Entity{{ID: "fixture-user", Name: "User", Kind: "person"}}
		fact := mc.Fact{ID: "concise-preference", Domain: "preferences", Subject: "fixture-user", Predicate: "prefers", Value: "concise replies", Status: "valid", SourceType: "user_statement", Sources: proposal.Sources, RecordedAt: proposal.Evidence[0].RecordedAt, Reason: "explicit user request"}
		toolDecision := memory.DecisionInput{Outcome: "applied", Reason: decision.Reason, Changes: []memory.ChangeInput{{Entry: memory.EntryInput{Entry: entry, Facts: []memory.FactInput{{Fact: fact, Qualifiers: []memory.QualifierInput{{Key: "context", Value: "replies"}}}}}}}}
		data, _ := json.Marshal(toolDecision)
		var args map[string]any
		_ = json.Unmarshal(data, &args)
		streamManagedTool(w, "memory_decide", args)
	})
	if err := memorypg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	repository = memorypg.New(f.pool)
	svc := &memory.Service{Repository: repository, Authority: f.authority}
	pki := filepath.Join(t.TempDir(), "pki")
	if err := platformrpc.CreateCredentials(pki); err != nil {
		t.Fatal(err)
	}
	ml := platformListener(t)
	ms, err := serverrpc.NewMemory(ml, platformrpc.CredentialsAt(pki, "memory"), svc, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, ms)
	mcRPC, err := memoryrpc.NewClient(ml.Addr().String(), platformrpc.CredentialsAt(pki, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return mcRPC.Health(ctx) == nil })
	gateway := managed.RuntimeApplications{Memory: mcRPC, Evidence: f.store}
	f.service.Applications = gateway
	rl := platformListener(t)
	rs, err := serverrpc.NewRuntime(rl, platformrpc.CredentialsAt(pki, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, rs)
	rc, err := runtimerpc.NewClient(rl.Addr().String(), platformrpc.CredentialsAt(pki, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool { return rc.Health(ctx) == nil })
	svc.Workers = managed.MemoryWorkers{Runtime: rc}
	stop := runApplicationFixture(t, f, gateway)
	input := f.submit(t, "remember", "", "Please remember that I prefer concise replies.")
	runtimeEventually(t, func() bool {
		var state string
		_ = f.pool.QueryRow(ctx, `SELECT state FROM runtime.inputs WHERE id=$1`, input.ID).Scan(&state)
		return state == "completed"
	})
	jobs, err := repository.PendingReviews(ctx, 100)
	if err != nil || len(jobs) != 1 {
		t.Fatal(jobs, err)
	}
	job := jobs[0]
	if job.WorkerID != "" || job.Receipt.Committed {
		t.Fatal("submission claimed commit", job)
	}
	// An accepted review survives stopping its source conversation.
	if err := f.service.Cancel(ctx, f.actor, f.tenant, f.agent.ID, f.main.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.Step(ctx); err != nil {
		t.Fatal(err)
	}
	runtimeEventually(t, func() bool {
		if err := svc.Step(ctx); err != nil {
			t.Error(err)
			return false
		}
		jobs, err := repository.PendingReviews(ctx, 100)
		return err == nil && len(jobs) == 0
	})
	stop()
	if mainCalls.Load() != 2 || workerCalls.Load() != 2 {
		t.Fatal("unexpected model budget", mainCalls.Load(), workerCalls.Load())
	}
	result, err := svc.Result(ctx, job.Scope.Access, f.main.ID, job.ID)
	if err != nil || !result.Committed {
		t.Fatal(result, err)
	}
	peer, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Knowledge reader", ModelID: f.agent.ModelID})
	if err != nil {
		t.Fatal(err)
	}
	access := job.Scope.Access
	access.AgentID = peer.ID
	page, err := svc.Search(ctx, access, mc.Query{Text: "concise"})
	if err != nil || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
	stored, err := svc.Read(ctx, access, mc.ReadRequest{ID: "concise-replies"})
	if err != nil || len(stored.Facts) != 1 || stored.Facts[0].Qualifiers["context"] != "replies" {
		t.Fatal("model qualifier conversion", stored, err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.application_jobs WHERE application='memory' AND job_id=$1`, job.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if _, err := svc.Decide(ctx, job.Scope, memory.Binding{ReviewID: job.ID, Epoch: job.Epoch, Fence: job.Fence}, mc.Decision{Outcome: "no_change", Reason: "second outcome"}, "new-command"); !errors.Is(err, application.ErrConflict) {
		t.Fatal("settled job mutated again", err)
	}
	// Reloading the app keeps the terminal Worker and knowledge receipts.
	reopened := &memory.Service{Repository: memorypg.New(f.pool), Authority: f.authority, Workers: svc.Workers}
	if err := reopened.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if page, err := reopened.Search(ctx, access, mc.Query{}); err != nil || len(page.Entries) != 1 {
		t.Fatal(page, err)
	}
}

type memoryLostAdmission struct {
	managed.ApplicationRuntime
	Lost bool
}

func (a *memoryLostAdmission) AdmitApplication(ctx context.Context, scope managedruntime.Scope, job managedruntime.ApplicationJob) (managedruntime.ApplicationReceipt, error) {
	value, err := a.ApplicationRuntime.AdmitApplication(ctx, scope, job)
	if err == nil && !a.Lost {
		a.Lost = true
		return managedruntime.ApplicationReceipt{}, errors.New("admission reply lost")
	}
	return value, err
}

func TestManagedMemoryWorkerAdmissionRecoveryAndDisable(t *testing.T) {
	ctx := context.Background()
	f := managedRuntimeHTTP(t, func(w http.ResponseWriter, r *http.Request) { t.Error("disabled review ran a model") })
	if err := memorypg.Migrate(ctx, f.pool); err != nil {
		t.Fatal(err)
	}
	scope, err := f.authority.AuthorizeApplication(ctx, application.Access{ActorID: f.actor, TenantID: f.tenant, AgentID: f.agent.ID, UserID: f.actor}, true)
	if err != nil {
		t.Fatal(err)
	}
	fixture := memoryFixture{pool: f.pool, directory: f.directory, store: memorypg.New(f.pool), scope: scope, thread: f.main.ID, human: application.Access{ActorID: f.actor, TenantID: f.tenant, UserID: f.actor}}
	svc := &memory.Service{Repository: fixture.store, Authority: f.authority}
	fixture.service = svc
	f.service.Applications = managed.RuntimeApplications{Memory: svc, Evidence: f.store}
	lost := &memoryLostAdmission{ApplicationRuntime: f.service}
	svc.Workers = managed.MemoryWorkers{Runtime: lost}
	first, _ := fixture.propose(t, "unknown-admission")
	if err := svc.Step(ctx); err == nil {
		t.Fatal("expected lost admission reply")
	}
	// A new app instance finds the original Runtime receipt, without a fresh job.
	svc = &memory.Service{Repository: memorypg.New(f.pool), Authority: f.authority, Workers: managed.MemoryWorkers{Runtime: f.service}}
	if err := svc.Step(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err := fixture.store.PendingReviews(ctx, 100)
	if err != nil || len(jobs) != 1 || jobs[0].WorkerID == "" {
		t.Fatal(jobs, err)
	}
	firstWorker := jobs[0].WorkerID
	second, _ := fixture.propose(t, "cancel-before-admission")
	status, err := svc.Status(ctx, fixture.human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Configure(ctx, fixture.human, status.Version, false, mc.Basic); err != nil {
		t.Fatal(err)
	}
	if err := svc.Step(ctx); err != nil {
		t.Fatal(err)
	}
	jobs, err = fixture.store.PendingReviews(ctx, 100)
	if err != nil || len(jobs) != 0 {
		t.Fatal(jobs, err)
	}
	var count int
	if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM runtime.application_jobs WHERE application='memory' AND cancelled AND job_id=ANY($1::text[])`, []string{first.ReviewID, second.ReviewID}).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	var thread string
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(thread_id::text,'') FROM runtime.application_jobs WHERE job_id=$1`, first.ReviewID).Scan(&thread); err != nil || thread != firstWorker {
		t.Fatal(thread, err)
	}
	if err := f.pool.QueryRow(ctx, `SELECT COALESCE(thread_id::text,'') FROM runtime.application_jobs WHERE job_id=$1`, second.ReviewID).Scan(&thread); err != nil || thread != "" {
		t.Fatal("cancel-first created Worker", thread, err)
	}
	status, err = svc.Status(ctx, fixture.human)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Configure(ctx, fixture.human, status.Version, true, mc.Basic); err != nil {
		t.Fatal(err)
	}
	if err := svc.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if result, err := svc.Result(ctx, scope.Access, f.main.ID, second.ReviewID); err != nil || result.State != "rejected" {
		t.Fatal("reenable revived review", result, err)
	}
}

func TestManagedMemoryRuntimeEvidenceRejectsSystemInputs(t *testing.T) {
	pool, store, scope, main := runtimeDatabase(t)
	ctx := context.Background()
	input, err := store.AcceptInput(ctx, scope, managedruntime.InputRequest{RequestID: "human", Text: "Original user input"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := store.Claim(ctx, scope.AgentID, "evidence", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	work, err := store.BeginTurn(ctx, lease, scope, input.ID, runtimeConfig())
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.BeginAttempt(ctx, lease, work.TurnID, managedruntime.ModelRequest{MaxOutputTokens: work.Config.Models[work.ModelIndex].MaxOutput, System: "test", Messages: work.History})
	if err != nil {
		t.Fatal(err)
	}
	response := llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Blocks: []llm.Block{{Type: llm.BlockToolUse, ToolUseID: "evidence", ToolName: "memory_propose", Input: map[string]any{"key": "test", "text": "test", "reason": "test"}}}}, StopReason: llm.StopToolUse}
	if err := store.FinishAttempt(ctx, lease, attempt.ID, response, ""); err != nil {
		t.Fatal(err)
	}
	tool, err := store.ClaimTool(ctx, "evidence-tool")
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := store.ToolEvidence(ctx, tool)
	if err != nil || evidence.Text != "Original user input" || evidence.Generation != 1 {
		t.Fatal(evidence, err)
	}
	// Reclassifying the origin models a peer/observation/application admission;
	// the model's otherwise identical text must never become human evidence.
	if _, err := pool.Exec(ctx, `UPDATE runtime.inputs SET source='{"kind":"peer"}' WHERE id=$1`, input.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ToolEvidence(ctx, tool); !errors.Is(err, managedruntime.ErrDenied) {
		t.Fatal("peer became user evidence", err)
	}
	if err := store.CancelThread(ctx, scope, main.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ToolEvidence(ctx, tool); !errors.Is(err, managedruntime.ErrFence) {
		t.Fatal("cancelled tool exported evidence", err)
	}
}
