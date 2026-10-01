//go:build postgres

package e2e

import (
	"context"
	"testing"

	"github.com/juex-ai/juex/internal/calendar"
	calendarrpc "github.com/juex-ai/juex/internal/calendar/rpc"
	serverrpc "github.com/juex-ai/juex/internal/entrypoints/platformrpc"
	executionrpc "github.com/juex-ai/juex/internal/execution/rpc"
	"github.com/juex-ai/juex/internal/foundation/lifecycle"
	"github.com/juex-ai/juex/internal/foundation/platformrpc"
	runtimerpc "github.com/juex-ai/juex/internal/managedruntime/rpc"
	"github.com/juex-ai/juex/internal/memory"
	memoryrpc "github.com/juex-ai/juex/internal/memory/rpc"
)

func TestManagedPurgePrivateRPCRequiresManagementIdentity(t *testing.T) {
	f := managedPurge(t)
	ctx := context.Background()
	runtimeListener, executionListener, memoryListener, calendarListener := platformListener(t), platformListener(t), platformListener(t), platformListener(t)
	r, err := serverrpc.NewRuntime(runtimeListener, platformrpc.CredentialsAt(f.credentials, "runtime"), f.service, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, r)
	e, err := serverrpc.NewExecution(executionListener, platformrpc.CredentialsAt(f.credentials, "execution"), f.execution, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, e)
	m, err := serverrpc.NewMemory(memoryListener, platformrpc.CredentialsAt(f.credentials, "memory"), &memory.Service{Repository: f.memory, Authority: f.authority}, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, m)
	c, err := serverrpc.NewCalendar(calendarListener, platformrpc.CredentialsAt(f.credentials, "calendar"), &calendar.Service{Repository: f.calendar, Authority: f.authority}, f.pool.Ping)
	if err != nil {
		t.Fatal(err)
	}
	runPlatformRPC(t, c)
	credentials := platformrpc.CredentialsAt(f.credentials, "management")
	rc, err := runtimerpc.NewClient(runtimeListener.Addr().String(), credentials)
	if err != nil {
		t.Fatal(err)
	}
	ec, err := executionrpc.NewClient(executionListener.Addr().String(), credentials)
	if err != nil {
		t.Fatal(err)
	}
	mc, err := memoryrpc.NewClient(memoryListener.Addr().String(), credentials)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := calendarrpc.NewClient(calendarListener.Addr().String(), credentials)
	if err != nil {
		t.Fatal(err)
	}
	f.purger.Services = map[string]lifecycle.Participant{"runtime": rc, "execution": ec, "memory": mc, "calendar": cc}
	job := f.archiveAndPurge(t)
	request := lifecycle.Request{Target: job.Target, Phase: lifecycle.Erase}
	for name, client := range f.purger.Services {
		v, err := client.Purge(ctx, request)
		if err != nil || !v.DataRemoved {
			t.Fatal(name, v, err)
		}
		if _, err = client.Purge(ctx, lifecycle.Request{}); err == nil {
			t.Fatal(name, "accepted missing identity")
		}
	}
	untrustedRuntime, err := runtimerpc.NewClient(runtimeListener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "memory"))
	if err != nil {
		t.Fatal(err)
	}
	untrustedExecution, err := executionrpc.NewClient(executionListener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	untrustedMemory, err := memoryrpc.NewClient(memoryListener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	untrustedCalendar, err := calendarrpc.NewClient(calendarListener.Addr().String(), platformrpc.CredentialsAt(f.credentials, "runtime"))
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []lifecycle.Participant{untrustedRuntime, untrustedExecution, untrustedMemory, untrustedCalendar} {
		if _, err := client.Purge(ctx, request); err == nil {
			t.Fatal("non-Management identity admitted cleanup")
		}
	}
}
