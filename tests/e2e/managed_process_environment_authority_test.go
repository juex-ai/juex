//go:build postgres

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	managementwire "github.com/juex-ai/juex/internal/foundation/platformrpc/wire/platform/management"
	"github.com/juex-ai/juex/internal/management"
)

func TestPrivateEnvironmentAuthorityFanoutAndServiceRole(t *testing.T) {
	f := executionDatabase(t)
	ctx := context.Background()
	second, err := f.directory.CreateAgent(ctx, f.actor, f.tenant, f.actor, management.AgentConfig{Name: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	member, err := f.directory.CreateUser(ctx, "private-member@example.test")
	if err != nil {
		t.Fatal(err)
	}
	_, token, err := f.directory.Invite(ctx, f.actor, f.tenant, member.Email, management.Member, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.directory.AcceptInvitation(ctx, member.ID, token); err != nil {
		t.Fatal(err)
	}
	other, err := f.directory.CreateAgent(ctx, member.ID, f.tenant, member.ID, management.AgentConfig{Name: "Other owner"})
	if err != nil {
		t.Fatal(err)
	}
	set := func(actor, agent, layer, value string) {
		t.Helper()
		view, err := f.directory.ProcessEnvironment(ctx, actor, f.tenant, agent)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.directory.ConfigureProcessEnvironment(ctx, actor, f.tenant, agent, layer, management.ProcessEnvironmentChange{Version: view.Layers[layer].Version, Set: map[string]string{"TOKEN": value}})
		if err != nil {
			t.Fatal(err)
		}
	}
	scope := func(actor, agent string) management.ModelCallScope {
		t.Helper()
		a, err := f.directory.AuthorizeAgent(ctx, actor, f.tenant, agent)
		if err != nil {
			t.Fatal(err)
		}
		return modelCallScope(a)
	}
	set(f.actor, second.ID, "agent", "second-private")
	a, b, c := scope(f.actor, f.agent.ID), scope(f.actor, second.ID), scope(member.ID, other.ID)
	set(f.actor, f.agent.ID, "fleet", "fleet-private")
	if scope(f.actor, f.agent.ID).AgentExecutionEpoch <= a.AgentExecutionEpoch || scope(f.actor, second.ID).AgentExecutionEpoch != b.AgentExecutionEpoch || scope(member.ID, other.ID).AgentExecutionEpoch != c.AgentExecutionEpoch {
		t.Fatal("Fleet update escaped owner or ignored override")
	}
	a = scope(f.actor, f.agent.ID)
	set(f.actor, f.agent.ID, "tenant", "tenant-private")
	if scope(f.actor, f.agent.ID).AgentExecutionEpoch != a.AgentExecutionEpoch || scope(f.actor, second.ID).AgentExecutionEpoch != b.AgentExecutionEpoch || scope(member.ID, other.ID).AgentExecutionEpoch <= c.AgentExecutionEpoch {
		t.Fatal("Tenant fanout did not fence only changed Agents")
	}
	for _, tc := range []struct{ actor, agent, want string }{{f.actor, f.agent.ID, "fleet-private"}, {f.actor, second.ID, "second-private"}, {member.ID, other.ID, "tenant-private"}} {
		values, err := f.directory.ResolveProcessEnvironment(ctx, management.ProcessEnvironmentAccess{Scope: scope(tc.actor, tc.agent), EnvironmentID: "same-device", WorkingDirectory: "/same/root"})
		if err != nil || values["TOKEN"] != tc.want {
			t.Fatal("Agent private values mixed", err)
		}
	}
	for _, layer := range []string{"tenant", "fleet", "workspace", "agent"} {
		if _, err := f.directory.ConfigureProcessEnvironment(ctx, member.ID, f.tenant, f.agent.ID, layer, management.ProcessEnvironmentChange{}); !errors.Is(err, management.ErrDenied) {
			t.Fatal("non-owner write", layer, err)
		}
	}
	if _, err := f.directory.ProcessEnvironment(ctx, member.ID, f.tenant, f.agent.ID); !errors.Is(err, management.ErrDenied) {
		t.Fatal("non-owner metadata", err)
	}
	if _, err := f.directory.ConfigureProcessEnvironment(ctx, member.ID, f.tenant, other.ID, "tenant", management.ProcessEnvironmentChange{Version: 1, Set: map[string]string{"TOKEN": "forbidden"}}); !errors.Is(err, management.ErrDenied) {
		t.Fatal("member Tenant write", err)
	}
	set(member.ID, other.ID, "fleet", "member-fleet")
	set(member.ID, other.ID, "agent", "member-agent")
	foreign, err := f.directory.CreateTenant(ctx, "Foreign", f.actor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.ProcessEnvironment(ctx, f.actor, foreign.ID, f.agent.ID); !errors.Is(err, management.ErrDenied) {
		t.Fatal("cross-Tenant read", err)
	}
	forged := scope(f.actor, f.agent.ID)
	forged.ActorID = member.ID
	if _, err := f.directory.ResolveProcessEnvironment(ctx, management.ProcessEnvironmentAccess{Scope: forged}); !errors.Is(err, management.ErrDenied) {
		t.Fatal("forged owner resolve", err)
	}
	listener := platformListener(t)
	server, err := serverrpc.NewManagement(listener, platformrpc.CredentialsAt(f.credentials, "management"), f.authority)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, server)
	raw, _ := json.Marshal(management.ProcessEnvironmentAccess{Scope: scope(f.actor, f.agent.ID)})
	for _, role := range []string{"execution", "runtime", "memory", "calendar"} {
		options, err := platformrpc.ClientOptions(listener.Addr().String(), "management", platformrpc.CredentialsAt(f.credentials, role))
		if err != nil {
			t.Fatal(err)
		}
		client, err := managementwire.NewClient("management", options...)
		if err != nil {
			t.Fatal(err)
		}
		reply, err := client.ResolveProcessEnvironment(ctx, string(raw))
		if err != nil {
			t.Fatal(role, err)
		}
		if role == "execution" {
			if reply.Code != "ok" {
				t.Fatal("Execution rejected", reply.Code)
			}
		} else if reply.Code != "denied" {
			t.Fatal("non-Execution private RPC", role, reply.Code)
		}
	}
	stale := scope(member.ID, other.ID)
	if _, err := f.directory.ChangeMember(ctx, f.actor, f.tenant, member.ID, management.Member, management.Suspended); err != nil {
		t.Fatal(err)
	}
	if _, err := f.directory.ResolveProcessEnvironment(ctx, management.ProcessEnvironmentAccess{Scope: stale}); !errors.Is(err, management.ErrDenied) {
		t.Fatal("suspended member resolved secret", err)
	}
}
