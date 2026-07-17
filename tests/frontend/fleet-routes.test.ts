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
    agentPagePath("agent one/blue", "/sessions/session one"),
    "/agents/agent%20one%2Fblue/sessions/session%20one",
  );
  assert.equal(
    agentBasePath("/agents/agent%20one/runtime"),
    "/agents/agent%20one",
  );
  assert.equal(
    agentPathFromLocation(
      "/observables/item%201",
      "/agents/agent%20one/history",
    ),
    "/agents/agent%20one/observables/item%201",
  );
});

test("agent switcher preserves stable sections but not entity ids", () => {
  assert.equal(
    agentSwitchPath("beta", "/agents/alpha/runtime"),
    "/agents/beta/runtime",
  );
  assert.equal(
    agentSwitchPath("beta", "/agents/alpha/observables/item"),
    "/agents/beta/observables",
  );
  assert.equal(
    agentSwitchPath("beta", "/agents/alpha/sessions/session-one"),
    "/agents/beta",
  );
  assert.equal(agentSwitchPath("beta", "/"), "/agents/beta");
});

