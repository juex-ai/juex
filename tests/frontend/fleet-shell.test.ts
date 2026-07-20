import assert from "node:assert/strict";
import test from "node:test";
import {
  agentActionWarning,
  agentStatusText,
  agentStateLabel,
  agentTabFromPath,
  agentTabPath,
  agentVisualState,
  nextFleetRosterLifecycleAction,
  nextAgentLifecycleAction,
  resolveAgentSelection,
} from "../../frontend/src/lib/fleet-shell.ts";
import type { AgentActivity, AgentStatus } from "../../frontend/src/types.ts";

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

function activity(
  state: AgentActivity["state"],
  pendingInputCount: number,
  alias?: string,
): AgentActivity {
  return {
    state,
    pending_input_count: pendingInputCount,
    selected_status: alias
      ? {
          session: {
            id: "session-1",
            alias,
            state: "turn_active",
            working: true,
            pending_count: pendingInputCount,
            max_pending_inputs: 16,
            can_accept_input: true,
          },
          tools: [],
          token_usage: { input_tokens: 0, output_tokens: 0 },
        }
      : undefined,
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
        activity: activity("working", 2),
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
        activity: activity("working", 1, "Release prep"),
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
        activity: activity("working", 0),
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

test("agent lifecycle toggle retries failed runtimes from the sidebar and state bar", () => {
  assert.equal(nextAgentLifecycleAction(agent("idle", "healthy")), "stop");
  assert.equal(nextAgentLifecycleAction(agent("stopped", "stopped")), "start");
  assert.equal(nextAgentLifecycleAction(agent("failed", "unhealthy")), "start");
  assert.equal(nextAgentLifecycleAction(agent("ambiguous", "ambiguous")), "start");
});

test("fleet roster keeps stale-runtime cleanup available", () => {
  assert.equal(nextFleetRosterLifecycleAction(agent("idle", "healthy")), "stop");
  assert.equal(nextFleetRosterLifecycleAction(agent("stopped", "stopped")), "start");
  assert.equal(nextFleetRosterLifecycleAction(agent("failed", "unhealthy")), "stop");
  assert.equal(nextFleetRosterLifecycleAction(agent("ambiguous", "ambiguous")), "stop");
});

test("restart continuation failures produce an actionable fleet warning", () => {
  const restarted = {
    ...agent("alpha", "healthy"),
    resume: {
      required: true,
      sent: false,
      error: "provider unavailable",
    },
  };
  assert.equal(
    agentActionWarning("restart", restarted),
    "Agent restarted, but interrupted work was not resumed: provider unavailable",
  );
  assert.equal(agentActionWarning("stop", restarted), null);
  assert.equal(
    agentActionWarning("restart", {
      ...restarted,
      resume: { required: true, sent: true },
    }),
    null,
  );
});
