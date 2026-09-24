import assert from "node:assert/strict";
import test from "node:test";

import { orderFleetAgents, mergeFleetRoster } from "../../frontend/src/lib/fleet-roster.ts";
import type { AgentStatus } from "../../frontend/src/types.ts";

test("Fleet pins only the bound Supervisor and preserves ordinary order without mutation", () => {
  const agents = [
    { id: "ordinary", name: "Supervisor" },
    { id: "retired", name: "Supervisor", is_supervisor: false },
    { id: "current", name: "Renamed", is_supervisor: true, enabled: false, runtime_health: "stopped" },
    { id: "last" },
  ] as AgentStatus[];
  assert.deepEqual(orderFleetAgents(agents).map(a => a.id), ["current", "ordinary", "retired", "last"]);
  assert.deepEqual(agents.map(a => a.id), ["ordinary", "retired", "current", "last"]);
  const reset = mergeFleetRoster(agents, agents.map(a => ({ ...a, is_supervisor: a.id === "last" })));
  assert.deepEqual(orderFleetAgents(reset).map(a => a.id), ["last", "ordinary", "retired", "current"]);
  assert.deepEqual(orderFleetAgents([]), []);
  const removed = agents.map(a => ({ ...a, is_supervisor: false }));
  assert.deepEqual(orderFleetAgents(removed), removed);
});
