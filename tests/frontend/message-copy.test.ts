import assert from "node:assert/strict";
import test from "node:test";
import {
  compactSummaryText,
  messageGroupCopyText,
} from "../../frontend/src/lib/message-copy.ts";

test("compactSummaryText extracts the persisted compact summary", () => {
  assert.equal(
    compactSummaryText(
      "Context compacted automatically because the provider context window is nearing its limit.\n\nSummary of earlier conversation:\nold decisions",
    ),
    "old decisions",
  );
});

test("messageGroupCopyText joins copyable text blocks", () => {
  assert.equal(
    messageGroupCopyText({
      key: "m1",
      role: "user",
      pending: false,
      units: [
        { kind: "text", block: { type: "text", text: "first" } },
        { kind: "text", block: { type: "text", text: "second" } },
      ],
    }),
    "first\n\nsecond",
  );
});
