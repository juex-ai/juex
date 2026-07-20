import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { AgentViewModelStore } from "../../frontend/src/lib/agent-view-model-store.ts";
import type {
  AgentActivity,
  AgentRuntimeStatusSnapshot,
  AgentStatus,
} from "../../frontend/src/types.ts";

function runtimeStatus(
  cursor: string,
  state: AgentRuntimeStatusSnapshot["session"]["state"],
  sessionID = "session-1",
  updatedAt = "",
): AgentRuntimeStatusSnapshot {
  return {
    cursor,
    updated_at: updatedAt,
    session: {
      id: sessionID,
      alias: "Primary",
      state,
      working: state === "turn_active" || state === "draining_pending",
      pending_count: 0,
      max_pending_inputs: 16,
      can_accept_input: true,
    },
    turn:
      state === "turn_active"
        ? {
            id: "turn-1",
            state: "active",
            phase: "provider_iteration",
            streaming: true,
            started_at: "",
            updated_at: "",
          }
        : undefined,
    tools: [],
    token_usage: { input_tokens: 0, output_tokens: 0 },
  };
}

function agentActivity(
  status: AgentRuntimeStatusSnapshot,
  state: AgentActivity["state"] = status.session.working ? "working" : "idle",
): AgentActivity {
  return {
    state,
    pending_input_count: state === "working" ? status.session.pending_count : 0,
    selected_status: status,
  };
}

test("frontend activity policy stays backend-owned", () => {
  const source = readFileSync(
    new URL(
      "../../frontend/src/lib/agent-view-model-store.ts",
      import.meta.url,
    ),
    "utf8",
  );
  assert.doesNotMatch(source, /session\.working/);
  assert.doesNotMatch(source, /SessionRuntimeTurnActive|draining_pending/);
  assert.doesNotMatch(source, /state === "turn_active"/);
});

test("store preserves aggregate state even when selected session differs", () => {
  const store = new AgentViewModelStore();
  const selected = runtimeStatus("one", "turn_active");
  store.applyFleetEvent({
    type: "agent.status",
    agent_id: "agent-1",
    activity: agentActivity(selected, "idle"),
  });
  const base: AgentStatus = {
    id: "agent-1",
    enabled: true,
    autostart: true,
    binding: "bound",
    runtime_health: "healthy",
    runtime_present: true,
    process_alive: true,
    endpoint_reachable: true,
    endpoint_matched: true,
  };
  assert.equal(store.projectAgents([base])[0].activity?.state, "idle");
});

test("one store projects roster and fleet updates", () => {
  const store = new AgentViewModelStore();
  const agent: AgentStatus = {
    id: "agent-1",
    enabled: true,
    autostart: true,
    binding: "bound",
    runtime_health: "healthy",
    runtime_present: true,
    process_alive: true,
    endpoint_reachable: true,
    endpoint_matched: true,
    activity: agentActivity(runtimeStatus("1", "idle")),
  };
  store.seedAgents([agent]);
  assert.equal(store.projectAgents([agent])[0].activity?.state, "idle");

  store.applyFleetEvent({
    type: "agent.status",
    agent_id: "agent-1",
    activity: agentActivity(runtimeStatus("2", "turn_active")),
  });
  const projected = store.projectAgents([agent])[0];
  assert.equal(projected.activity?.state, "working");
  assert.equal(projected.activity?.selected_status?.turn?.streaming, true);
});

test("roster polling corrects stale fleet stream activity", () => {
  const store = new AgentViewModelStore();
  const base: AgentStatus = {
    id: "agent-1",
    enabled: true,
    autostart: true,
    binding: "bound",
    runtime_health: "healthy",
    runtime_present: true,
    process_alive: true,
    endpoint_reachable: true,
    endpoint_matched: true,
  };
  store.applyFleetEvent({
    type: "agent.status",
    agent_id: "agent-1",
    activity: agentActivity(runtimeStatus("stream-1", "turn_active")),
  });
  assert.equal(store.projectAgents([base])[0].activity?.state, "working");

  const polled = {
    ...base,
    activity: agentActivity(runtimeStatus("poll-2", "idle")),
  };
  store.seedAgents([polled]);

  const projected = store.projectAgents([polled])[0];
  assert.equal(projected.activity?.state, "idle");
  assert.equal(projected.activity?.selected_status?.cursor, "poll-2");
});

test("healthy roster polling preserves stream activity when activity is omitted", () => {
  const store = new AgentViewModelStore();
  const agent: AgentStatus = {
    id: "agent-1",
    enabled: true,
    autostart: true,
    binding: "bound",
    runtime_health: "healthy",
    runtime_present: true,
    process_alive: true,
    endpoint_reachable: true,
    endpoint_matched: true,
  };
  store.applyFleetEvent({
    type: "agent.status",
    agent_id: "agent-1",
    activity: agentActivity(
      runtimeStatus("stream-working", "turn_active"),
    ),
  });

  store.seedAgents([agent]);

  const projected = store.projectAgents([agent])[0];
  assert.equal(projected.activity?.state, "working");
  assert.equal(projected.activity?.selected_status?.cursor, "stream-working");
});

test("session streams do not replace the fleet-selected session", () => {
  const store = new AgentViewModelStore();
  const selected = runtimeStatus("fleet-1", "turn_active", "session-1");
  store.applyFleetEvent({
    type: "agent.status",
    agent_id: "agent-1",
    activity: agentActivity(selected),
  });
  store.setStatus("agent-1", selected);

  const historical = runtimeStatus("history-1", "idle", "session-2");
  store.setStatus("agent-1", historical);

  const projected = store.projectAgents([
    {
      id: "agent-1",
      enabled: true,
      autostart: true,
      binding: "bound",
      runtime_health: "healthy",
      runtime_present: true,
      process_alive: true,
      endpoint_reachable: true,
      endpoint_matched: true,
    },
  ])[0];
  assert.equal(projected.activity?.selected_status?.session.id, "session-1");
  assert.equal(store.status("agent-1", "session-2")?.cursor, "history-1");

  store.applyFleetEvent({
    type: "agent.status",
    agent_id: "agent-1",
    activity: agentActivity(
      runtimeStatus("fleet-2", "idle", "session-1"),
    ),
  });
  assert.equal(store.status("agent-1", "session-1")?.cursor, "fleet-1");
  assert.equal(store.status("agent-1", "session-2")?.cursor, "history-1");
  store.clearStatus("agent-1", "session-1");
  assert.equal(store.status("agent-1", "session-1"), undefined);
  assert.equal(
    store.projectAgents([projected])[0].activity?.selected_status?.cursor,
    "fleet-2",
  );
});

test("stream delivery order wins over updated_at timestamps", () => {
  const store = new AgentViewModelStore();
  store.applyFleetEvent({
    type: "agent.status",
    agent_id: "agent-1",
    activity: agentActivity(
      runtimeStatus(
        "cursor-1",
        "turn_active",
        "session-1",
        "2026-07-19T00:00:02Z",
      ),
    ),
  });
  store.applyFleetEvent({
    type: "agent.status",
    agent_id: "agent-1",
    activity: agentActivity(
      runtimeStatus(
        "cursor-2",
        "idle",
        "session-1",
        "2026-07-19T00:00:01Z",
      ),
    ),
  });

  const projected = store.projectAgents([
    {
      id: "agent-1",
      enabled: true,
      autostart: true,
      binding: "bound",
      runtime_health: "healthy",
      runtime_present: true,
      process_alive: true,
      endpoint_reachable: true,
      endpoint_matched: true,
    },
  ])[0];
  assert.equal(projected.activity?.state, "idle");
  assert.equal(projected.activity?.selected_status?.cursor, "cursor-2");

  store.setStatus(
    "agent-1",
    runtimeStatus(
      "direct-1",
      "turn_active",
      "session-1",
      "2026-07-19T00:00:02Z",
    ),
  );
  store.setStatus(
    "agent-1",
    runtimeStatus(
      "direct-2",
      "idle",
      "session-1",
      "2026-07-19T00:00:01Z",
    ),
  );
  assert.equal(store.status("agent-1", "session-1")?.cursor, "direct-2");
});
