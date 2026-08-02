package fleet

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/juex-ai/juex/internal/agentstate"
	"github.com/juex-ai/juex/internal/endpoint"
	"github.com/juex-ai/juex/internal/processmetrics"
)

type stubProcessUsageSampler struct {
	usage   processmetrics.Usage
	err     error
	samples []processSampleCall
	forgot  []string
	retain  []string
}

type processSampleCall struct {
	key string
	pid int
}

func (s *stubProcessUsageSampler) Sample(
	_ context.Context,
	key string,
	pid int,
) (processmetrics.Usage, error) {
	s.samples = append(s.samples, processSampleCall{key: key, pid: pid})
	return s.usage, s.err
}

func (s *stubProcessUsageSampler) Forget(key string) {
	s.forgot = append(s.forgot, key)
}

func (s *stubProcessUsageSampler) Retain(keys []string) {
	s.retain = append([]string(nil), keys...)
}

func TestInspectStatusAddsUsageOnlyForVerifiedHealthyProcess(t *testing.T) {
	runtimeState := processMetricsRuntime()
	deps := processMetricsStatusDependencies(runtimeState)
	cpu := 125.5
	metrics := &stubProcessUsageSampler{
		usage: processmetrics.Usage{RSSBytes: 96_000_000, CPUPercent: &cpu},
	}
	manager := &Manager{
		homeDir:        t.TempDir(),
		probeTimeout:   time.Second,
		processMetrics: metrics,
		deps:           deps,
	}

	status := manager.inspectStatus(
		context.Background(),
		registryEntry(runtimeState.AgentID, "agent"),
	)
	if status.Process == nil ||
		status.Process.RSSBytes != 96_000_000 ||
		status.Process.CPUPercent == nil ||
		*status.Process.CPUPercent != cpu {
		t.Fatalf("process usage = %+v", status.Process)
	}
	if len(metrics.samples) != 1 ||
		metrics.samples[0] != (processSampleCall{key: runtimeState.AgentID, pid: runtimeState.PID}) {
		t.Fatalf("samples = %+v", metrics.samples)
	}

	deps.probe = func(context.Context, endpoint.Runtime) error {
		return &endpoint.IdentityMismatchError{
			Expected: runtimeState,
			Actual:   endpoint.Runtime{AgentID: runtimeState.AgentID, InstanceID: "other"},
		}
	}
	metrics.samples = nil
	manager.deps = deps
	mismatched := manager.inspectStatus(
		context.Background(),
		registryEntry(runtimeState.AgentID, "agent"),
	)
	if mismatched.Process != nil || len(metrics.samples) != 0 {
		t.Fatalf("mismatched status/samples = %+v/%+v", mismatched, metrics.samples)
	}
}

func TestInspectStatusIgnoresUsageFailure(t *testing.T) {
	runtimeState := processMetricsRuntime()
	deps := processMetricsStatusDependencies(runtimeState)
	metrics := &stubProcessUsageSampler{err: errors.New("access denied")}
	manager := &Manager{
		homeDir:        t.TempDir(),
		probeTimeout:   time.Second,
		processMetrics: metrics,
		deps:           deps,
	}

	status := manager.inspectStatus(
		context.Background(),
		registryEntry(runtimeState.AgentID, "agent"),
	)
	if status.RuntimeHealth != RuntimeHealthy || status.Process != nil || status.Problem != "" {
		t.Fatalf("status = %+v", status)
	}
}

func TestStatusPrunesRemovedAgentMetricBaselines(t *testing.T) {
	entries := []agentstate.RegistryEntry{
		registryEntry("aaaaaa", "alpha"),
		registryEntry("bbbbbb", "beta"),
	}
	deps := defaultDependencies()
	deps.listRegistry = func(string) ([]agentstate.RegistryEntry, error) {
		return entries, nil
	}
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
		return endpoint.Runtime{}, errors.New("unavailable")
	}
	metrics := &stubProcessUsageSampler{}
	manager := &Manager{
		homeDir:        t.TempDir(),
		processMetrics: metrics,
		deps:           deps,
	}

	if _, err := manager.Status(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(metrics.retain) != 2 ||
		metrics.retain[0] != "aaaaaa" ||
		metrics.retain[1] != "bbbbbb" {
		t.Fatalf("retained keys = %v", metrics.retain)
	}
}

func processMetricsRuntime() endpoint.Runtime {
	return endpoint.Runtime{
		AgentID:    "aaaaaa",
		InstanceID: "instance-one",
		PID:        42,
		Endpoint:   "tcp://127.0.0.1:43123",
		StartedAt:  time.Date(2026, 7, 17, 8, 0, 0, 0, time.UTC),
	}
}

func processMetricsStatusDependencies(runtimeState endpoint.Runtime) dependencies {
	deps := defaultDependencies()
	deps.inspectBinding = func(agentstate.RegistryEntry) agentstate.WorkspaceBinding {
		return agentstate.WorkspaceBinding{Kind: agentstate.WorkspaceBound}
	}
	deps.readRuntime = func(agentstate.AgentAddress) (endpoint.Runtime, error) {
		return runtimeState, nil
	}
	deps.processAlive = func(int) (bool, error) { return true, nil }
	deps.probe = func(context.Context, endpoint.Runtime) error { return nil }
	return deps
}
