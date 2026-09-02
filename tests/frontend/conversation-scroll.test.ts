import test from "node:test";
import assert from "node:assert/strict";

import {
  threadComposerClearance,
  threadConversationScrollOptions,
} from "../../frontend/src/lib/conversation-scroll.ts";

test("threadConversationScrollOptions jumps through initial hydration", () => {
  assert.deepEqual(threadConversationScrollOptions(), {
    initial: "instant",
    resize: "instant",
  });
});

test("threadConversationScrollOptions smooths live follow-up resize", () => {
  assert.deepEqual(threadConversationScrollOptions("live"), {
    initial: "instant",
    resize: "smooth",
  });
});

test("threadComposerClearance keeps a 150px floor and reserves the 48px fade", () => {
  assert.equal(threadComposerClearance(0), 150);
  assert.equal(threadComposerClearance(Number.NaN), 150);
  assert.equal(threadComposerClearance(80), 150);
  assert.equal(threadComposerClearance(124), 172);
  assert.equal(threadComposerClearance(180.2), 229);
});
