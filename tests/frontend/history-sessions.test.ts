import test from "node:test";
import assert from "node:assert/strict";

import {
  historySessionBadges,
  historySessionHref,
  historySessionTitle,
} from "../../frontend/src/lib/history-sessions.ts";
import { sessionPreviewTitle } from "../../frontend/src/lib/session-title.ts";

test("historySessionHref routes sessions through the canonical session view", () => {
  assert.equal(
    historySessionHref("primary/session 1"),
    "/sessions/primary%2Fsession%201",
  );
  assert.equal(
    historySessionHref(
      "primary/session 1",
      "/agents/agent%20one/history",
    ),
    "/agents/agent%20one/sessions/primary%2Fsession%201",
  );
});

test("historySessionTitle falls back for empty previews", () => {
  assert.equal(historySessionTitle({ preview: "" }), "New Session");
  assert.equal(historySessionTitle({ preview: "   " }), "New Session");
  assert.equal(historySessionTitle({ preview: "status check" }), "status check");
});

test("sessionPreviewTitle handles missing preview values", () => {
  assert.equal(sessionPreviewTitle(null), "New Session");
  assert.equal(sessionPreviewTitle(undefined), "New Session");
});

test("historySessionBadges keeps kind and active state as metadata only", () => {
  assert.deepEqual(
    historySessionBadges({ kind: "primary", active: true, turns: 3 }),
    ["primary", "active", "3 turns"],
  );
  assert.deepEqual(
    historySessionBadges({ kind: "side", active: false, turns: 1 }),
    ["side", "1 turn"],
  );
});
