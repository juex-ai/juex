import test from "node:test";
import assert from "node:assert/strict";

import {
  sessionCanSend,
  sessionReadOnlyMessage,
} from "../../frontend/src/lib/session-access.ts";

test("sessionCanSend only allows normal active primary sessions", () => {
  assert.equal(sessionCanSend({ kind: "primary", active: true }), true);
  assert.equal(sessionCanSend({ kind: "primary", active: false }), false);
  assert.equal(sessionCanSend({ kind: "side", active: false }), false);
});

test("sessionReadOnlyMessage explains inactive and side sessions", () => {
  assert.equal(
    sessionReadOnlyMessage({ kind: "primary", active: false }),
    "Inactive primary session",
  );
  assert.equal(sessionReadOnlyMessage({ kind: "side", active: false }), "Side session");
});
