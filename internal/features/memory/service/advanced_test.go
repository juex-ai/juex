package service

import (
	"context"
	"testing"
	"time"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
)

func contribute(t *testing.T, s *Store, c mc.Caller, ended []string) mc.SourceState {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Participation(ctx, c, mc.Boundary{ThreadID: "0", Epoch: "on-1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ref := mc.Source{FleetID: c.FleetID, AgentID: c.AgentID, ThreadID: "0", GenerationID: "g000001", From: 1, Through: 2}
	out, err := s.Contribute(ctx, c, mc.SourceBatch{ThreadID: "0", Epoch: "on-1", Through: 2, EndedGenerations: ended, IdleSince: s.now().Add(-time.Minute), Evidence: []mc.Evidence{{Source: ref, Kind: "user", Text: "The release branch is stable", RecordedAt: s.now()}}})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestAdvancedGenerationThresholdAndNoChangeProgress(t *testing.T) {
	s, a, super, _ := fixture(t)
	ctx := context.Background()
	var err error
	s, err = Open(s.dir, a.FleetID, mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	contribute(t, s, a, []string{"g1", "g2", "g3", "g4"})
	job, err := s.Claim(ctx, super)
	if err != nil || job != nil {
		t.Fatalf("four Generations triggered: %+v %v", job, err)
	}
	_, err = s.Contribute(ctx, a, mc.SourceBatch{ThreadID: "0", Epoch: "on-1", After: 2, Through: 2, EndedGenerations: []string{"g1", "g2", "g3", "g4", "g5"}, IdleSince: s.now().Add(-time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	job, err = s.Claim(ctx, super)
	if err != nil || job == nil || !job.Automatic {
		t.Fatalf("five Generations: %+v %v", job, err)
	}
	worker := super
	worker.AssignmentID = job.ID
	worker.Token = job.Token
	if _, err = s.Decide(ctx, worker, mc.Decision{Outcome: "no_change", Reason: "No durable addition"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Participation(ctx, a, mc.Boundary{ThreadID: "0", Epoch: "on-1", Enabled: true})
	if err != nil || got.ProcessedThrough != 2 {
		t.Fatalf("no-change cursor %+v %v", got, err)
	}
}

func TestAdvancedMaximumWaitFailureAndOptOut(t *testing.T) {
	s, a, super, _ := fixture(t)
	ctx := context.Background()
	s, err := Open(s.dir, a.FleetID, mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	contribute(t, s, a, nil)
	now := s.now()
	s.now = func() time.Time { return now.Add(25 * time.Hour) }
	if _, err := s.Participation(ctx, a, mc.Boundary{ThreadID: "0", Epoch: "on-1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	job, err := s.Claim(ctx, super)
	if err != nil || job == nil {
		t.Fatalf("max wait %+v %v", job, err)
	}
	worker := super
	worker.AssignmentID = job.ID
	worker.Token = job.Token
	if _, err := s.Fail(ctx, worker, mc.Failure{Reason: "provider unavailable"}); err != nil {
		t.Fatal(err)
	}
	state, err := s.Participation(ctx, a, mc.Boundary{ThreadID: "0", Epoch: "on-1", Enabled: true})
	if err != nil || state.ProcessedThrough != 0 {
		t.Fatalf("failure advanced cursor %+v %v", state, err)
	}
	if _, err := s.Participation(ctx, a, mc.Boundary{ThreadID: "0", Epoch: "off-2", Cursor: 10, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	state, err = s.Participation(ctx, a, mc.Boundary{ThreadID: "0", Epoch: "on-3", Cursor: 25, Enabled: true})
	if err != nil || state.ProcessedThrough != 25 {
		t.Fatalf("opt-out gap %+v %v", state, err)
	}
	if _, err := s.Contribute(ctx, a, mc.SourceBatch{ThreadID: "0", Epoch: "on-1", After: 2, Through: 10}); err == nil {
		t.Fatal("old epoch accepted")
	}
}

func TestStructuredTemporalKnowledgeAndStrategySwitch(t *testing.T) {
	s, a, super, user := fixture(t)
	ctx := context.Background()
	ref := mc.Source{FleetID: a.FleetID, AgentID: a.AgentID, ThreadID: "0", GenerationID: "g000001", From: 1, Through: 2}
	e := entry("profile")
	e.Type = "user"
	e.Sources = []mc.Source{ref}
	e.Entities = []mc.Entity{{ID: "person-1", Name: "Alex", Kind: "person"}, {ID: "person-2", Name: "Alex", Kind: "person"}}
	e.Facts = []mc.Fact{{Subject: "person-1", Predicate: "mbti", Value: "INTP", Status: "valid", SourceType: "derived", Sources: []mc.Source{ref}, RecordedAt: s.now()}}
	if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: "bad", Action: "correct", Changes: []mc.Change{{Entry: e}}}); err == nil {
		t.Fatal("inferred MBTI accepted")
	}
	e.Facts[0].SourceType = "self_report"
	if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: "good", Action: "correct", Changes: []mc.Change{{Entry: e}}}); err != nil {
		t.Fatal(err)
	}
	s, err := Open(s.dir, a.FleetID, mc.Advanced)
	if err != nil {
		t.Fatal(err)
	}
	contribute(t, s, a, []string{"g1", "g2", "g3", "g4", "g5"})
	job, err := s.Claim(ctx, super)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	s, err = Open(s.dir, a.FleetID, mc.Basic)
	if err != nil {
		t.Fatal(err)
	}
	worker := super
	worker.AssignmentID = job.ID
	worker.Token = job.Token
	if _, err := s.Decide(ctx, worker, mc.Decision{Outcome: "no_change"}); err == nil {
		t.Fatal("old strategy assignment committed")
	}
	got, err := s.Read(ctx, a, mc.ReadRequest{ID: e.ID})
	if err != nil || len(got.Entities) != 2 || len(got.Facts) != 1 {
		t.Fatalf("Basic lost structured knowledge %+v %v", got, err)
	}
	r, err := s.Recall(ctx, a, mc.Query{Text: "profile"})
	if err != nil || len(r.Entries) != 0 || r.Strategy != mc.Basic {
		t.Fatalf("Basic automatic recall %+v %v", r, err)
	}
}
