import test from "node:test";
import assert from "node:assert/strict";

import {
  agentBasePath,
  agentPagePath,
  agentPathFromLocation,
  agentSwitchPath,
} from "../../frontend/src/lib/fleet-routes.ts";

test("agent route helpers encode ids and preserve agent-local paths", () => {
  assert.equal(
    agentPagePath("agent one/blue", "/threads/thread one"),
    "/agents/agent%20one%2Fblue/threads/thread%20one",
  );
  assert.equal(
    agentBasePath("/agents/agent%20one/runtime"),
    "/agents/agent%20one",
  );
  assert.equal(
    agentPathFromLocation(
      "/runtime/observables/item%201",
      "/agents/agent%20one/history",
    ),
    "/agents/agent%20one/runtime/observables/item%201",
  );
});

test("agent switcher preserves stable sections but not entity ids", () => {
  assert.equal(
    agentSwitchPath("beta", "/agents/alpha/runtime"),
    "/agents/beta/runtime",
  );
  assert.equal(
    agentSwitchPath("beta", "/agents/alpha/runtime/extensions"),
    "/agents/beta/runtime/extensions",
  );
  assert.equal(
    agentSwitchPath("beta", "/agents/alpha/runtime/observables/item"),
    "/agents/beta/runtime/observables",
  );
  assert.equal(
    agentSwitchPath("beta", "/agents/alpha/threads/thread-one"),
    "/agents/beta",
  );
  assert.equal(agentSwitchPath("beta", "/"), "/agents/beta");
});
