import test from "node:test";
import assert from "node:assert/strict";

import {
  threadCanSend,
  threadReadOnlyMessage,
} from "../../frontend/src/lib/thread-access.ts";

test("threadCanSend allows active Main and Worker threads", () => {
  assert.equal(threadCanSend({ active: true }), true);
  assert.equal(threadCanSend({ active: false }), false);
});

test("threadReadOnlyMessage explains archived threads", () => {
  assert.equal(threadReadOnlyMessage({ active: true }), "");
  assert.equal(threadReadOnlyMessage({ active: false }), "Archived thread");
});
