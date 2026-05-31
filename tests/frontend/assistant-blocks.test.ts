import assert from "node:assert/strict";
import test from "node:test";
import { assistantBlocksFromEventPayload } from "../../frontend/src/lib/assistant-blocks.ts";

test("assistantBlocksFromEventPayload prefers ordered canonical blocks", () => {
  const blocks = assistantBlocksFromEventPayload({
    thinking: "flattened thinking",
    text: "flattened text",
    tool_calls: [
      { tool_use_id: "legacy", name: "grep", input: { pattern: "x" } },
    ],
    blocks: [
      { type: "text", text: "lead text" },
      {
        type: "tool_use",
        tool_use_id: "tu1",
        tool_name: "read",
        input: { path: "README.md" },
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
    },
    { type: "reasoning", text: "reason after tool" },
    { type: "text", text: "tail text" },
  ]);
});

test("assistantBlocksFromEventPayload keeps legacy llm.responded payloads working", () => {
  const blocks = assistantBlocksFromEventPayload({
    thinking: "legacy thinking",
    text: "legacy answer",
    tool_calls: [{ tool_use_id: "tu1", name: "read", input: { path: "x" } }],
  });

  assert.deepEqual(blocks, [
    { type: "reasoning", text: "legacy thinking" },
    { type: "text", text: "legacy answer" },
    {
      type: "tool_use",
      tool_use_id: "tu1",
      tool_name: "read",
      input: { path: "x" },
    },
  ]);
});
