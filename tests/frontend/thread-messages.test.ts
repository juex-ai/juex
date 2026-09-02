import assert from "node:assert/strict";
import test from "node:test";
import {
  mergeOlderThreadPage,
  messageCreatedAtFromID,
} from "../../frontend/src/lib/thread-messages.ts";
import type { ThreadShowResponse } from "../../frontend/src/types.ts";

function threadPage(
  overrides: Partial<ThreadShowResponse>,
): ThreadShowResponse {
  return {
    id: "thread",
    dir: "/tmp/thread",
    kind: "primary",
    active: true,
    started_at: "2026-05-07T10:10:10Z",
    last_active_at: "2026-05-07T10:10:10Z",
    turns: 1,
    preview: "current",
    token_usage: { input_tokens: 1, output_tokens: 2 },
    messages: [],
    ...overrides,
  };
}

test("messageCreatedAtFromID decodes only canonical timestamped message IDs", () => {
  assert.equal(
    messageCreatedAtFromID("msg-20260820T214228-abcdef12"),
    "2026-08-20T21:42:28Z",
  );
  for (const id of [
    undefined,
    "msg-user",
    "msg-20261320T214228-abcdef12",
    "msg-20260820T214228-ABCDEF12",
    "turn-20260820T214228-abcdef12",
  ]) {
    assert.equal(messageCreatedAtFromID(id), undefined);
  }
});

test("mergeOlderThreadPage prepends messages without overwriting live metadata", () => {
  const current = threadPage({
    last_active_at: "2026-05-07T10:20:00Z",
    turns: 5,
    token_usage: { input_tokens: 50, output_tokens: 60 },
    messages: [
      { id: "m3", role: "user", blocks: [{ type: "text", text: "new" }] },
    ],
    has_more_before: true,
    oldest_message_id: "m3",
  });
  const older = threadPage({
    last_active_at: "2026-05-07T10:10:00Z",
    turns: 2,
    token_usage: { input_tokens: 10, output_tokens: 20 },
    messages: [
      { id: "m1", role: "user", blocks: [{ type: "text", text: "old" }] },
      {
        id: "m2",
        role: "assistant",
        blocks: [{ type: "text", text: "older" }],
      },
    ],
    has_more_before: false,
    oldest_message_id: "m1",
  });

  const merged = mergeOlderThreadPage(current, older);

  assert.equal(merged.last_active_at, "2026-05-07T10:20:00Z");
  assert.equal(merged.turns, 5);
  assert.deepEqual(merged.token_usage, { input_tokens: 50, output_tokens: 60 });
  assert.deepEqual(
    merged.messages.map((message) => message.id),
    ["m1", "m2", "m3"],
  );
  assert.equal(merged.has_more_before, false);
  assert.equal(merged.oldest_message_id, "m1");
});
