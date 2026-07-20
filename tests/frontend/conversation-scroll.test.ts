import test from "node:test";
import assert from "node:assert/strict";

import {
  sessionComposerClearance,
  sessionConversationScrollOptions,
} from "../../frontend/src/lib/conversation-scroll.ts";

test("sessionConversationScrollOptions jumps through initial hydration", () => {
  assert.deepEqual(sessionConversationScrollOptions(), {
    initial: "instant",
    resize: "instant",
  });
});

test("sessionConversationScrollOptions smooths live follow-up resize", () => {
  assert.deepEqual(sessionConversationScrollOptions("live"), {
    initial: "instant",
    resize: "smooth",
  });
});

test("sessionComposerClearance keeps a 150px floor and reserves the 48px fade", () => {
  assert.equal(sessionComposerClearance(0), 150);
  assert.equal(sessionComposerClearance(Number.NaN), 150);
  assert.equal(sessionComposerClearance(80), 150);
  assert.equal(sessionComposerClearance(124), 172);
  assert.equal(sessionComposerClearance(180.2), 229);
});
