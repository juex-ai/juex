import test from "node:test";
import assert from "node:assert/strict";

import { sessionConversationScrollOptions } from "../../frontend/src/lib/conversation-scroll.ts";

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
