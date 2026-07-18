import assert from "node:assert/strict";
import test from "node:test";
import {
  agentStatusText,
  agentStateLabel,
  agentTabFromPath,
  agentTabPath,
  agentVisualState,
  nextAgentLifecycleAction,
  resolveAgentSelection,
} from "../../frontend/src/lib/fleet-shell.ts";
import type { AgentStatus } from "../../frontend/src/types.ts";

function agent(
  id: string,
  runtimeHealth: AgentStatus["runtime_health"],
  overrides: Partial<AgentStatus> = {},
): AgentStatus {
  return {
    id,
    enabled: true,
    autostart: false,
    binding: "bound",
    runtime_health: runtimeHealth,
    runtime_present: runtimeHealth !== "stopped",
    process_alive: runtimeHealth === "healthy",
    endpoint_reachable: runtimeHealth === "healthy",
    endpoint_matched: runtimeHealth === "healthy",
    ...overrides,
  };
}

test("agent visual state distinguishes process and live activity", () => {
  assert.equal(agentVisualState(agent("stopped", "stopped")), "stopped");
  assert.equal(agentVisualState(agent("failed", "unhealthy")), "failed");
  assert.equal(agentVisualState(agent("ambiguous", "ambiguous")), "failed");
  assert.equal(agentVisualState(agent("idle", "healthy")), "idle");
  assert.equal(
    agentVisualState(
      agent("working", "healthy", {
        activity: { state: "working", pending_count: 2 },
      }),
    ),
    "working",
  );
});

test("agent status text uses concise operational context", () => {
  assert.equal(agentStatusText(agent("stopped", "stopped")), "Stopped");
  assert.equal(
    agentStatusText(
      agent("failed", "unhealthy", { problem: "Endpoint did not respond" }),
    ),
    "Endpoint did not respond",
  );
  assert.equal(
    agentStatusText(
      agent("working", "healthy", {
        activity: {
          state: "working",
          pending_count: 1,
          session_alias: "Release prep",
        },
      }),
    ),
    "Working · Release prep",
  );
  assert.equal(
    agentStatusText(agent("idle", "healthy", { workspace: "/work/juex" })),
    "Idle · juex",
  );
});

test("agent state labels stay compact when failure details are long", () => {
  assert.equal(agentStateLabel(agent("stopped", "stopped")), "Stopped");
  assert.equal(
    agentStateLabel(
      agent("failed", "unhealthy", {
        runtime_health: "unhealthy",
        problem: "a very long diagnostic that belongs in the failure banner",
      }),
    ),
    "Failed",
  );
  assert.equal(
    agentStateLabel(
      agent("working", "healthy", {
        runtime_health: "healthy",
        activity: { state: "working", pending_count: 0 },
      }),
    ),
    "Working",
  );
});

test("selection restores a valid saved agent then falls back to the first", () => {
  const agents = [agent("alpha", "healthy"), agent("beta", "stopped")];
  assert.equal(resolveAgentSelection(agents, "beta"), "beta");
  assert.equal(resolveAgentSelection(agents, "missing"), "alpha");
  assert.equal(resolveAgentSelection([], "alpha"), null);
});

test("tab routing remounts existing selected-agent pages", () => {
  assert.equal(agentTabFromPath("/agents/alpha/sessions/s1"), "chat");
  assert.equal(agentTabFromPath("/agents/alpha/history"), "chat");
  assert.equal(agentTabFromPath("/agents/alpha/runtime"), "runtime");
  assert.equal(agentTabFromPath("/agents/alpha/observables/weekly"), "observables");
  assert.equal(agentTabFromPath("/agents/alpha/logs"), "logs");
  assert.equal(agentTabFromPath("/agents/alpha/config"), "config");

  assert.equal(agentTabPath("alpha", "chat"), "/agents/alpha");
  assert.equal(agentTabPath("alpha", "runtime"), "/agents/alpha/runtime");
  assert.equal(
    agentTabPath("alpha", "observables"),
    "/agents/alpha/observables",
  );
});

test("single lifecycle toggle stops healthy agents and starts other states", () => {
  assert.equal(nextAgentLifecycleAction(agent("idle", "healthy")), "stop");
  assert.equal(nextAgentLifecycleAction(agent("stopped", "stopped")), "start");
  assert.equal(nextAgentLifecycleAction(agent("failed", "unhealthy")), "start");
});
