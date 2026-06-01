import assert from "node:assert/strict";
import test from "node:test";
import { mergeOlderSessionPage } from "../../frontend/src/lib/session-messages.ts";
import type { SessionShowResponse } from "../../frontend/src/types.ts";

function sessionPage(
  overrides: Partial<SessionShowResponse>,
): SessionShowResponse {
  return {
    id: "session",
    dir: "/tmp/session",
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

test("mergeOlderSessionPage prepends messages without overwriting live metadata", () => {
  const current = sessionPage({
    last_active_at: "2026-05-07T10:20:00Z",
    turns: 5,
    token_usage: { input_tokens: 50, output_tokens: 60 },
    messages: [
      { id: "m3", role: "user", blocks: [{ type: "text", text: "new" }] },
    ],
    has_more_before: true,
    oldest_message_id: "m3",
  });
  const older = sessionPage({
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

  const merged = mergeOlderSessionPage(current, older);

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
