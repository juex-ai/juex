import test from "node:test";
import assert from "node:assert/strict";

import {
  threadCanSend,
  threadReadOnlyMessage,
} from "../../frontend/src/lib/thread-access.ts";

test("threadCanSend allows active Main and Worker threads", () => {
  assert.equal(threadCanSend({ retention_state: "active" }), true);
  assert.equal(threadCanSend({ retention_state: "archived" }), false);
});

test("threadReadOnlyMessage explains archived threads", () => {
  assert.equal(threadReadOnlyMessage({ retention_state: "active" }), "");
  assert.equal(threadReadOnlyMessage({ retention_state: "archived" }), "Archived thread");
});
