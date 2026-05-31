import test from "node:test";
import assert from "node:assert/strict";

import {
  historySessionBadges,
  historySessionHref,
  historySessionTitle,
} from "../../frontend/src/lib/history-sessions.ts";

test("historySessionHref routes sessions through the read-only history viewer", () => {
  assert.equal(
    historySessionHref("primary/session 1"),
    "/history/sessions/primary%2Fsession%201",
  );
});

test("historySessionTitle falls back for empty previews", () => {
  assert.equal(historySessionTitle({ preview: "" }), "(empty)");
  assert.equal(historySessionTitle({ preview: "status check" }), "status check");
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
