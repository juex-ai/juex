import test from "node:test";
import assert from "node:assert/strict";

import {
  threadListBadges,
  threadHref,
  threadListTitle,
} from "../../frontend/src/lib/thread-list.ts";

test("threadHref routes threads through the canonical thread view", () => {
  assert.equal(
    threadHref("primary/thread 1"),
    "/threads/primary%2Fthread%201",
  );
  assert.equal(
    threadHref(
      "primary/thread 1",
      "/agents/agent%20one/threads",
    ),
    "/agents/agent%20one/threads/primary%2Fthread%201",
  );
});

test("threadListTitle combines alias and id", () => {
  assert.equal(threadListTitle({ alias: "main", thread_id: "0" }), "main · #0");
  assert.equal(threadListTitle({ alias: "", thread_id: "123456" }), "Thread · #123456");
});

test("threadListBadges shows lifecycle and generation counts", () => {
  assert.deepEqual(
    threadListBadges({ retention_state: "active", turn_count: 3, generation_count: 2 }),
    ["active", "3 turns", "2 gen"],
  );
  assert.deepEqual(
    threadListBadges({ retention_state: "archived", turn_count: 1, generation_count: 1 }),
    ["archived", "1 turn", "1 gen"],
  );
});
