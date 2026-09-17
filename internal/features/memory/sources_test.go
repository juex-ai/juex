package memory

import (
	"context"
	"errors"
	"testing"

	memoryservice "github.com/juex-ai/juex/internal/features/memory/service"
	"github.com/juex-ai/juex/internal/foundation/events"
	"github.com/juex-ai/juex/internal/foundation/llm"
	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	runtimemodule "github.com/juex-ai/juex/internal/framework/module"
	"github.com/juex-ai/juex/internal/framework/thread"
)

type capturedContribution struct {
	mc.API
	batches chan mc.SourceBatch
}

func (a *capturedContribution) Contribute(ctx context.Context, c mc.Caller, b mc.SourceBatch) (mc.SourceState, error) {
	a.batches <- b
	return a.API.Contribute(ctx, c, b)
}

func TestSourcePollCannotOverwriteNewerActiveState(t *testing.T) {
	agentDir := t.TempDir()
	th, err := thread.NewStore(agentDir).EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = th.Close() }()
	if err := ConfigureParticipation(agentDir, true); err != nil {
		t.Fatal(err)
	}
	s, err := memoryservice.Open(t.TempDir(), "fleet", mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	api := &capturedContribution{API: s, batches: make(chan mc.SourceBatch, 8)}
	caller := mc.Caller{FleetID: "fleet", AgentID: "agent", ThreadID: th.ID, Profile: mc.ProfileAgent}
	paused, resume := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-resume:
		default:
			close(resume)
		}
	}()
	poll := &SourceFeed{AgentDir: agentDir, Client: func(string) (mc.API, mc.Caller) {
		close(paused)
		<-resume
		return api, caller
	}}
	done := make(chan error, 1)
	go func() { done <- poll.Poll(t.Context()) }()
	<-paused
	if err := th.AppendEvent(events.Event{Type: "turn.started", TurnID: "active"}); err != nil {
		t.Fatal(err)
	}
	active := &SourceFeed{AgentDir: agentDir, Client: func(string) (mc.API, mc.Caller) { return api, caller }}
	err = active.MarkActive(t.Context(), th.ID)
	close(resume)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	close(api.batches)
	var last mc.SourceBatch
	for batch := range api.batches {
		last = batch
	}
	if last.Pending == 0 {
		t.Fatal("poll replaced active source state with its stale idle inspection")
	}
}

func TestProposalRetryKeepsAdmittedEvidenceStable(t *testing.T) {
	th, err := thread.NewStore(t.TempDir()).EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = th.Close() }()
	store, err := memoryservice.Open(t.TempDir(), "fleet", mc.Basic)
	if err != nil {
		t.Fatal(err)
	}
	caller := mc.Caller{FleetID: "fleet", AgentID: "source", ThreadID: th.ID, Profile: mc.ProfileAgent}
	m := New(Options{API: store, Caller: caller, Thread: th})
	message := llm.TextMessage(llm.RoleUser, "Remember my stable release preference")
	message.ID = "admitted-message"
	if err := m.PrepareInput(t.Context(), runtimemodule.InputPreparationRequest{PreparationID: "turn/input", Message: message}); err != nil {
		t.Fatal(err)
	}
	if err := th.Append(message); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"key": "stable", "text": "Release on Tuesday", "reason": "explicit request"}
	first, err := m.propose(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if err := th.Append(llm.TextMessage(llm.RoleAssistant, "Retrying the same submission after uncertain delivery")); err != nil {
		t.Fatal(err)
	}
	again, err := m.propose(t.Context(), input)
	if err != nil || again != first {
		t.Fatalf("retry changed request evidence: first=%s again=%s err=%v", first, again, err)
	}
}

type lostContribution struct {
	mc.API
	lost bool
}

func (a *lostContribution) Contribute(ctx context.Context, c mc.Caller, b mc.SourceBatch) (mc.SourceState, error) {
	state, err := a.API.Contribute(ctx, c, b)
	if err == nil && len(b.Evidence) > 0 && !a.lost {
		a.lost = true
		return mc.SourceState{}, errors.New("response lost after source acceptance")
	}
	return state, err
}

func TestSourceFeedLostResponseAndParticipationGap(t *testing.T) {
	agentDir := t.TempDir()
	store := thread.NewStore(agentDir)
	th, err := store.EnsureMain()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = th.Close() }()
	appendText := func(text string) uint64 {
		t.Helper()
		if err := th.AppendBatch([]llm.Message{llm.TextMessage(llm.RoleUser, text)}); err != nil {
			t.Fatal(err)
		}
		return th.Projection().EventCursor.Seq
	}
	old := appendText("before participation")
	if err := ConfigureParticipation(agentDir, true); err != nil {
		t.Fatal(err)
	}
	s, err := memoryservice.Open(t.TempDir(), "fleet", mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	api := &lostContribution{API: s}
	caller := mc.Caller{FleetID: "fleet", AgentID: "agent", ThreadID: "0", Profile: mc.ProfileAgent}
	client := func(id string) (mc.API, mc.Caller) { c := caller; c.ThreadID = id; return api, c }
	feed := &SourceFeed{AgentDir: agentDir, Client: client}
	included := appendText("stable included preference")
	if err := feed.Poll(context.Background()); err == nil {
		t.Fatal("lost response not surfaced")
	}
	if err := feed.Poll(context.Background()); err != nil {
		t.Fatalf("lost response reconciliation: %v", err)
	}
	ref := func(seq uint64) mc.Source {
		return mc.Source{FleetID: "fleet", AgentID: "agent", ThreadID: "0", GenerationID: thread.InitialGeneration, From: seq, Through: seq}
	}
	if _, err := s.History(context.Background(), caller, ref(old)); err == nil {
		t.Fatal("backfilled history before participation")
	}
	if got, err := s.History(context.Background(), caller, ref(included)); err != nil || len(got) != 1 {
		t.Fatalf("included source %+v %v", got, err)
	}
	if err := ConfigureParticipation(agentDir, false); err != nil {
		t.Fatal(err)
	}
	if err := SyncDisabled(context.Background(), agentDir, client); err != nil {
		t.Fatal(err)
	}
	excluded := appendText("opted-out private message")
	if err := ConfigureParticipation(agentDir, true); err != nil {
		t.Fatal(err)
	}
	includedAgain := appendText("new participating preference")
	if err := feed.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.History(context.Background(), caller, ref(excluded)); err == nil {
		t.Fatal("backfilled opted-out interval")
	}
	if _, err := s.History(context.Background(), caller, ref(includedAgain)); err != nil {
		t.Fatal(err)
	}
}
