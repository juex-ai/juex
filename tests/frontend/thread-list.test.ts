import test from "node:test";
import assert from "node:assert/strict";

import {
  threadBatchGroups,
  threadListBadges,
  threadHref,
  threadListTitle,
} from "../../frontend/src/lib/thread-list.ts";

test("threadBatchGroups settles descendants first across the complete index without reordering siblings", () => {
  const root = { thread_id: "0" };
  const parent = { thread_id: "parent", parent_thread_id: "0" };
  const intermediate = { thread_id: "archived", parent_thread_id: "parent" };
  const child = { thread_id: "child", parent_thread_id: "archived" };
  const sibling = { thread_id: "sibling", parent_thread_id: "0" };
  const targets = [parent, sibling, child];
  const index = new Map([root, parent, intermediate, child, sibling].map((thread) => [thread.thread_id, thread]));
  assert.deepEqual(threadBatchGroups(targets, index), [[child], [parent, sibling]]);
  assert.deepEqual(targets, [parent, sibling, child]);
  assert.deepEqual(threadBatchGroups([], index), []);
});

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
