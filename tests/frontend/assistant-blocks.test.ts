import assert from "node:assert/strict";
import test from "node:test";
import { assistantBlocksFromEventPayload } from "../../frontend/src/lib/assistant-blocks.ts";

test("assistantBlocksFromEventPayload prefers ordered canonical blocks", () => {
  const blocks = assistantBlocksFromEventPayload({
    thinking: "flattened thinking",
    text: "flattened text",
    tool_calls: [
      { tool_use_id: "flat-call", name: "grep", input: { pattern: "x" } },
    ],
    blocks: [
      { type: "text", text: "lead text" },
      {
        type: "tool_use",
        tool_use_id: "tu1",
        tool_name: "read",
        input: { path: "README.md" },
        timeout_seconds: 60,
      },
      {
        type: "image",
        media: {
			artifact_path: "sessions/s/media/image.png",
          media_type: "image/png",
          sha256: "abc",
          original_bytes: 12,
          width: 2,
          height: 3,
        },
      },
      { type: "reasoning", text: "reason after tool" },
      { type: "text", text: "tail text" },
    ],
  });

  assert.deepEqual(blocks, [
    { type: "text", text: "lead text" },
    {
      type: "tool_use",
      tool_use_id: "tu1",
      tool_name: "read",
      input: { path: "README.md" },
      timeout_seconds: 60,
    },
    {
      type: "image",
      media: {
		artifact_path: "sessions/s/media/image.png",
        media_type: "image/png",
        sha256: "abc",
        original_bytes: 12,
        width: 2,
        height: 3,
      },
    },
    { type: "reasoning", text: "reason after tool" },
    { type: "text", text: "tail text" },
  ]);
});

test("assistantBlocksFromEventPayload decodes flattened llm.responded payloads", () => {
  const blocks = assistantBlocksFromEventPayload({
    thinking: "flattened thinking",
    text: "flattened answer",
    tool_calls: [
      { tool_use_id: "tu1", name: "read", input: { path: "x" }, timeout_seconds: 30 },
    ],
  });

  assert.deepEqual(blocks, [
    { type: "reasoning", text: "flattened thinking" },
    { type: "text", text: "flattened answer" },
    {
      type: "tool_use",
      tool_use_id: "tu1",
      tool_name: "read",
      input: { path: "x" },
      timeout_seconds: 30,
    },
  ]);
});
