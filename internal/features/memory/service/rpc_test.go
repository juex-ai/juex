package service

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"testing"

	mc "github.com/juex-ai/juex/internal/foundation/memoryclient"
	"github.com/juex-ai/juex/internal/foundation/serviceendpoint"
)

type fixedResolver struct{ record serviceendpoint.Record }

func (r fixedResolver) Resolve(context.Context, string) (serviceendpoint.Record, error) {
	return r.record, nil
}

func TestTypedMemoryRPCIdentityProfilesAndReceipts(t *testing.T) {
	s, a, super, user := fixture(t)
	ctx := context.Background()
	listener, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	identity := serviceendpoint.Identity{FleetID: a.FleetID, ServiceID: "memory", InstanceID: "instance-a"}
	svr := serviceendpoint.ControlServer(listener, identity, func() {})
	if err := mc.Register(svr, identity, s); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- svr.Run() }()
	t.Cleanup(func() { _ = svr.Stop(); <-done })
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port))
	resolver := fixedResolver{serviceendpoint.Record{Identity: identity, Network: "tcp", Address: address}}
	client := mc.New(resolver, "memory", a)
	if _, err := client.Admin(ctx, a, mc.AdminRequest{Key: "bad", Action: "correct", Changes: []mc.Change{{Entry: entry("bad")}}}); err == nil {
		t.Fatal("ordinary profile administered")
	}
	if _, err := client.Status(ctx, user); err == nil {
		t.Fatal("client changed frozen profile")
	}
	r, err := client.Propose(ctx, a, mc.Proposal{Key: "rpc", Text: "remember release convention", Reason: "user request", Sources: []mc.Source{testSource()}})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := mc.New(resolver, "memory", super)
	job, err := coordinator.Claim(ctx, super)
	if err != nil || job == nil {
		t.Fatalf("claim %+v %v", job, err)
	}
	worker := super
	worker.AssignmentID = job.ID
	worker.Token = job.Token
	reviewer := mc.New(resolver, "memory", worker)
	if _, err := reviewer.Decide(ctx, worker, mc.Decision{Outcome: "applied", Changes: []mc.Change{{Entry: entry("shared")}}}); err != nil {
		t.Fatal(err)
	}
	got, err := client.Result(ctx, a, r.ID)
	if err != nil || !got.Committed {
		t.Fatalf("receipt %+v %v", got, err)
	}
	for i := 0; i < 50; i++ {
		e := entry(fmt.Sprintf("provenance-%02d", i))
		e.Sources = make([]mc.Source, 100)
		for j := range e.Sources {
			e.Sources[j] = testSource()
			e.Sources[j].From, e.Sources[j].Through = uint64(j+1), uint64(j+1)
		}
		if _, err := s.Admin(ctx, user, mc.AdminRequest{Key: e.ID, Action: "correct", Changes: []mc.Change{{Entry: e}}}); err != nil {
			t.Fatal(err)
		}
	}
	page, err := client.Search(ctx, a, mc.Query{Text: "provenance", Limit: 50})
	if err != nil || len(page.Entries) != 50 || page.Next != -1 {
		t.Fatalf("large provenance search: entries=%d next=%d err=%v", len(page.Entries), page.Next, err)
	}
	full, err := client.Read(ctx, a, mc.ReadRequest{ID: page.Entries[0].ID})
	if err != nil || len(full.Sources) != 100 {
		t.Fatalf("full provenance read: sources=%d err=%v", len(full.Sources), err)
	}
	resolver.record.InstanceID = "stale"
	stale := mc.New(resolver, "memory", a)
	if _, err := stale.Search(ctx, a, mc.Query{}); err == nil {
		t.Fatal("stale service accepted query")
	}
}
